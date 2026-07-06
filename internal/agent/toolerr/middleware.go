// Package toolerr provides a compose.ToolMiddleware that turns tool
// errors into ordinary tool messages so the agent's ReAct loop can keep
// going. Without this, a single tool failure (e.g. "workspace already
// exists; pick another slug") would bubble up as a fatal AgentEvent.Err
// and abort the whole stream — the model never sees the error text and
// can't recover.
//
// With this middleware installed, the tool's error becomes the tool's
// "result" content. The model reads it on the next ReAct turn and
// decides how to proceed (retry with a different slug, ask the user,
// give up gracefully, etc.) — same UX as ChatGPT / Claude Desktop's
// tool-error handling.
package toolerr

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// isInterruptErr reports whether err is a framework interrupt signal in any
// of the wire shapes eino uses to move interrupts around:
//
//   - compose.interruptError / compose.subGraphInterruptError — what the
//     compose graph runner wraps for its own reruns; ExtractInterruptInfo
//     catches these
//   - raw *adk.InterruptSignal (= *core.InterruptSignal) — what an
//     adk.NewAgentTool wrapper returns when a sub-agent interrupts
//     internally; ExtractInterruptInfo does NOT catch this, we have to
//     unwrap with errors.As directly
//
// Miss any of these and this middleware treats the interrupt as a normal
// tool failure, stringifying its checkpoint gob into the tool_result the
// model then reads back as text. That's the "red gob dump" symptom.
func isInterruptErr(err error) bool {
	if _, ok := compose.ExtractInterruptInfo(err); ok {
		return true
	}
	var sig *adk.InterruptSignal
	return errors.As(err, &sig)
}

// Middleware returns a ToolMiddleware that captures errors on both the
// invokable and streamable paths. Install it at the head of
// ToolsConfig.ToolCallMiddlewares for every agent that runs the ReAct
// loop.
func Middleware() compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				out, err := next(ctx, input)
				if err == nil {
					return out, nil
				}
				// Interrupt signals (from approval middleware or a tool that
				// itself calls tool.Interrupt) are framework control flow —
				// pass them through untouched so eino can capture the
				// interrupt info and emit an Interrupted event.
				if isInterruptErr(err) {
					return nil, err
				}
				clean := stripFrameworkWrappers(err.Error())
				FromContext(ctx).Record(input.CallID, clean)
				return &compose.ToolOutput{Result: formatToolErrMsg(input.Name, clean)}, nil
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				out, err := next(ctx, input)
				if err == nil {
					return out, nil
				}
				if isInterruptErr(err) {
					return nil, err
				}
				clean := stripFrameworkWrappers(err.Error())
				FromContext(ctx).Record(input.CallID, clean)
				return &compose.StreamToolOutput{
					Result: schema.StreamReaderFromArray([]string{formatToolErrMsg(input.Name, clean)}),
				}, nil
			}
		},
	}
}

// formatToolErrMsg produces a short, model-friendly explanation of a tool
// failure. The model reads this string on its next ReAct turn so it can
// react to the error rather than silently abort. `cleanErr` is the
// already-sanitized message (eino/compose wrappers stripped).
func formatToolErrMsg(toolName, cleanErr string) string {
	return fmt.Sprintf("工具 %s 调用失败：%s。请根据该错误调整参数后重试，或选择其他方式完成任务。", toolName, cleanErr)
}

// nodePathSuffix matches the trailing diagnostic that eino appends to
// graph-level errors, e.g. " ----- node path: [node_1, ToolNode]".
var nodePathSuffix = regexp.MustCompile(`\s*-{3,}\s*node path:.*$`)

func stripFrameworkWrappers(s string) string {
	s = nodePathSuffix.ReplaceAllString(s, "")
	if i := strings.LastIndex(s, "err="); i >= 0 {
		s = s[i+len("err="):]
	}
	return strings.TrimSpace(s)
}

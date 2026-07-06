package approval

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

// Middleware wraps tool calls with the approval decision chain. Order is:
//
//  1. destructive tools (rm / drop / etc.) → ALWAYS prompt, even in
//     full_access. See destructive.go.
//  2. full_access → skip approval entirely (post-destructive so an
//     elevated mode can't green-light data loss).
//  3. policy.NeedsApproval says no → run normally.
//  4. resume: apply the user's decision (allow → next; deny → denial JSON).
//  5. auto mode:
//     a. IsSafeAuto (fast path rules)   → next
//     b. Classifier (LLM) says allow    → next
//     ↳ anything else falls through.
//  6. Otherwise (default mode, or auto that fell through): tool.Interrupt
//     to pause for the human.
//
// Wire it OUTSIDE toolerr.Middleware so the interrupt sentinel error bubbles
// straight up to the framework instead of being wrapped into a fake tool
// result:
//
//	ToolCallMiddlewares: []compose.ToolMiddleware{
//	    approval.Middleware(store, classifier),
//	    toolerr.Middleware(),
//	}
func Middleware(store *ModeStore, classifier *Classifier) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				decision := evaluate(ctx, store, classifier, input.Name, input.Arguments)
				switch decision.kind {
				case decisionPass:
					return next(ctx, input)
				case decisionDeny:
					return &compose.ToolOutput{Result: denialMessage(input.Name, decision.reason)}, nil
				default:
					return nil, tool.Interrupt(ctx, &stream.ApprovalInfo{
						Tool:   input.Name,
						Args:   input.Arguments,
						CallID: input.CallID,
					})
				}
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				decision := evaluate(ctx, store, classifier, input.Name, input.Arguments)
				switch decision.kind {
				case decisionPass:
					return next(ctx, input)
				case decisionDeny:
					return &compose.StreamToolOutput{
						Result: schema.StreamReaderFromArray([]string{denialMessage(input.Name, decision.reason)}),
					}, nil
				default:
					return nil, tool.Interrupt(ctx, &stream.ApprovalInfo{
						Tool:   input.Name,
						Args:   input.Arguments,
						CallID: input.CallID,
					})
				}
			}
		},
	}
}

// decisionKind partitions the middleware's answers into three categories
// the Invokable/Streamable wrappers can act on uniformly. Both wrappers
// share evaluate() to keep the decision chain in one place.
type decisionKind int

const (
	decisionInterrupt decisionKind = iota // prompt the user
	decisionPass                          // run the tool
	decisionDeny                          // return the user's rejection reason
)

type decision struct {
	kind   decisionKind
	reason string // for decisionDeny: user reason surfaced to the model
}

func evaluate(ctx context.Context, store *ModeStore, classifier *Classifier, toolName, argsJSON string) decision {
	// (1) Destructive wall — applies to every mode including full_access.
	if isDest, why := IsDestructive(toolName, argsJSON); isDest {
		if resumed, dec, ok := resumeDecision(ctx); resumed {
			if ok && !dec.Approved {
				return decision{kind: decisionDeny, reason: dec.Reason}
			}
			return decision{kind: decisionPass}
		}
		log.Printf("approval: destructive gate held %s (%s)", toolName, why)
		return decision{kind: decisionInterrupt}
	}

	mode := store.Get(contextkey.ConversationID(ctx))

	// (2) full_access — post-destructive so elevation can't skip the wall.
	if mode == ModeFullAccess {
		return decision{kind: decisionPass}
	}

	// (3) Policy says no approval needed.
	if !NeedsApproval(toolName, argsJSON) {
		return decision{kind: decisionPass}
	}

	// (4) Already resumed with a decision.
	if resumed, dec, ok := resumeDecision(ctx); resumed {
		if !ok || dec.Approved {
			// No decision attached: treat as approved so implicit-resume-all
			// flows (Runner.Resume without params) don't deadlock.
			return decision{kind: decisionPass}
		}
		return decision{kind: decisionDeny, reason: dec.Reason}
	}

	// (5) auto mode: fast path → classifier → fall through.
	if mode == ModeAuto {
		if ok, why := IsSafeAuto(toolName, argsJSON); ok {
			log.Printf("approval: auto fastpath allowed %s (%s)", toolName, why)
			return decision{kind: decisionPass}
		}
		if classifier.IsAvailable() {
			verdict, err := classifier.Classify(ctx, toolName, argsJSON, "")
			if err != nil {
				log.Printf("approval: classifier failed for %s: %v (reason=%s)", toolName, err, verdict.Reason)
			}
			if verdict.Approved {
				log.Printf("approval: auto classifier allowed %s (%s)", toolName, verdict.Reason)
				return decision{kind: decisionPass}
			}
			log.Printf("approval: auto classifier deferred %s (%s)", toolName, verdict.Reason)
		}
		// Fall through to interrupt.
	}

	return decision{kind: decisionInterrupt}
}

// resumeDecision distills tool.GetInterruptState + tool.GetResumeContext
// into: (is this a resume?, decision, did the user attach one?).
func resumeDecision(ctx context.Context) (bool, Decision, bool) {
	interrupted, _, _ := tool.GetInterruptState[any](ctx)
	if !interrupted {
		return false, Decision{}, false
	}
	_, has, dec := tool.GetResumeContext[Decision](ctx)
	return true, dec, has
}

// denialMessage is what the model sees on its next ReAct turn when a call is
// rejected. Returns a stable JSON shape so the model can distinguish a user
// cancellation ("canceled":true) from a real tool error, and act on
// "instruction" instead of retrying the same call.
func denialMessage(toolName, reason string) string {
	payload := map[string]any{
		"canceled": true,
		"tool":     toolName,
	}
	reason = strings.TrimSpace(reason)
	if reason != "" {
		payload["reason"] = reason
		payload["instruction"] = "用户拒绝执行该工具并给出了 reason。请根据 reason 调整方案，不要原样重试同一工具调用。"
	} else {
		payload["instruction"] = "用户拒绝执行该工具但未说明理由。请向用户说明该操作已取消，并询问希望如何继续，或提出不执行该工具的替代方案。"
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

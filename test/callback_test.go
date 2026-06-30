package test

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/llm"
	"github.com/guyi-a/Interview-Agent/internal/config"
)

func newChain(t *testing.T) (compose.Runnable[[]*schema.Message, *schema.Message], context.Context) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	ctx := context.Background()
	cm, err := llm.NewChatModel(ctx, cfg.LLM)
	if err != nil {
		t.Fatalf("llm.NewChatModel: %v", err)
	}

	r, err := compose.NewChain[[]*schema.Message, *schema.Message]().
		AppendChatModel(cm, compose.WithNodeName("interview_llm")).
		Compile(ctx)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return r, ctx
}

func newPrintHandler(t *testing.T, tag string) callbacks.Handler {
	t.Helper()
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if mi := model.ConvCallbackInput(input); mi != nil {
				t.Logf("[%s][START] component=%s name=%s messages=%d", tag, info.Component, info.Name, len(mi.Messages))
			} else {
				t.Logf("[%s][START] component=%s name=%s", tag, info.Component, info.Name)
			}
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if mo := model.ConvCallbackOutput(output); mo != nil && mo.Message != nil {
				usage := ""
				if mo.TokenUsage != nil {
					usage = " usage=" + tokenUsageString(mo.TokenUsage)
				}
				t.Logf("[%s][END]   component=%s name=%s reply_len=%d%s", tag, info.Component, info.Name, len(mo.Message.Content), usage)
			} else {
				t.Logf("[%s][END]   component=%s name=%s", tag, info.Component, info.Name)
			}
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			t.Logf("[%s][ERROR] component=%s name=%s err=%v", tag, info.Component, info.Name, err)
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, sr *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			t.Logf("[%s][STREAM-OUT-OPEN] component=%s name=%s", tag, info.Component, info.Name)
			go func() {
				defer sr.Close()
				var chunks int
				var content strings.Builder
				for {
					raw, err := sr.Recv()
					if errors.Is(err, io.EOF) {
						t.Logf("[%s][STREAM-OUT-EOF] component=%s name=%s chunks=%d total_len=%d", tag, info.Component, info.Name, chunks, content.Len())
						return
					}
					if err != nil {
						t.Logf("[%s][STREAM-OUT-ERR] component=%s err=%v", tag, info.Component, err)
						return
					}
					chunks++
					if mo := model.ConvCallbackOutput(raw); mo != nil && mo.Message != nil {
						content.WriteString(mo.Message.Content)
					}
				}
			}()
			return ctx
		}).
		Build()
}

func tokenUsageString(u *model.TokenUsage) string {
	if u == nil {
		return ""
	}
	return "prompt=" + strconv.Itoa(u.PromptTokens) +
		" completion=" + strconv.Itoa(u.CompletionTokens) +
		" total=" + strconv.Itoa(u.TotalTokens)
}

func TestChainInvokeWithCallback(t *testing.T) {
	r, ctx := newChain(t)
	h := newPrintHandler(t, "INV")

	out, err := r.Invoke(ctx,
		[]*schema.Message{schema.UserMessage("用一句中文介绍 Go 语言最大的优点。")},
		compose.WithCallbacks(h),
	)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out == nil || out.Content == "" {
		t.Fatal("empty reply")
	}
	t.Logf("=== final reply ===\n%s", out.Content)
}

func TestChainStreamWithCallback(t *testing.T) {
	r, ctx := newChain(t)
	h := newPrintHandler(t, "STM")

	sr, err := r.Stream(ctx,
		[]*schema.Message{schema.UserMessage("用中文说出 1 到 5 这五个数字，用空格分隔。")},
		compose.WithCallbacks(h),
	)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	var full strings.Builder
	for {
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		full.WriteString(chunk.Content)
	}
	if full.Len() == 0 {
		t.Fatal("empty stream reply")
	}
	t.Logf("=== final reply ===\n%s", full.String())
}

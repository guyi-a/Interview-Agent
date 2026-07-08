package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/hitl"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

// AskUserInput 是 ask_user 工具的入参。
//
// 一次调用可以问 1-5 个问题，每题 2-5 个选项。多题会在同一屏对用户呈现（UI
// 内部走 tab 切换），用户答完一批后 run 恢复。选项字符串末尾追加
// "(Recommended)" / "（推荐）" 可标记推荐项，前端识别后自动抠掉后缀并高亮。
type AskUserInput struct {
	Questions []hitl.Question `json:"questions" jsonschema:"description=1-5 个问题。每个 Question 需要 question 正文 + 2-5 个 options（字符串数组）。想标记推荐项就在 option 字符串末尾追加 (Recommended) 或 （推荐），前端会识别。multi_select=true 时为多选（checkbox，至少选一个）；默认 false 单选（radio）。label 可选，多题时作为 UI tab 头部；id 可选，用于关联答案。"`
}

// askUserOutput 是工具的最终返回给 LLM 的字符串包装。
//
// InferTool 需要一个具体输出类型；实际返回给模型的只有 Text 字段，因此
// 序列化后 LLM 看到的就是一段普通文本，不用理解结构。
type askUserOutput struct {
	Text string `json:"text"`
}

// cancelledMessage 是用户点"暂不继续"后返回给 LLM 的固定文案。skill / tool
// description 里会教 agent 遇到这条应该用默认方案继续 / 提供替代 / 停止当前
// 分支，而不是傻等或原样重试。
const cancelledMessage = "用户暂不作答（取消了本次提问）。请用合理默认值继续，或改用替代方案，或停止当前分支，不要再次调用 ask_user 重问同一件事。"

func newAskUserTool() (tool.BaseTool, error) {
	desc := "Ask the user one or more multiple-choice questions and pause the run until they answer. " +
		"Use this ONLY when a missing/ambiguous input would clearly send the run down a wrong branch — " +
		"if a reasonable default exists, USE the default and mention it in the reply instead of asking. " +
		"Do NOT batch trivial preference polling; keep it to one focused question set per fork. " +
		"Each question needs question text + 2-10 option strings (mark a recommended one by appending ' (Recommended)' / '（推荐）'). " +
		"Set multi_select=true when the user may pick more than one. " +
		"Do NOT poll the user for something that you can decide from context or a sane default; " +
		"do NOT ask the user to confirm your plan (users approve tool calls separately via the approval system). " +
		"On cancel the tool returns a marker string — do not retry the same question."

	fn := func(ctx context.Context, in *AskUserInput) (*askUserOutput, error) {
		// 无论首次调用还是恢复重跑，都先 normalize —— 恢复时 in.Questions 是
		// agent 传的原始参数（可能没 id），如果不 normalize，renderAnswers
		// 里按 id 找答案就会全部落到"未回答"分支。
		normalized, err := normalizeQuestions(in.Questions)
		if err != nil {
			return nil, err
		}

		// 恢复回合：检测到之前 Interrupt 过，直接取答案返回给 LLM。
		wasInterrupted, _, _ := tool.GetInterruptState[any](ctx)
		if wasInterrupted {
			_, hasAnswers, answers := tool.GetResumeContext[hitl.Answers](ctx)
			if !hasAnswers {
				// 未附答案的隐式 resume：视为取消。避免死循环重问。
				return &askUserOutput{Text: cancelledMessage}, nil
			}
			if answers.Cancelled {
				return &askUserOutput{Text: cancelledMessage}, nil
			}
			return &askUserOutput{Text: renderAnswers(normalized, answers)}, nil
		}

		// 首次进入：抛 Interrupt 让 runner 保存 checkpoint，等 HTTP 层带答案
		// resume 后再进入上面的分支。CallID 目前留空 —— 前端底部通用 dock
		// 展示，不做工具卡内联对齐（后续 UX 优化可以补）。
		return nil, tool.Interrupt(ctx, &stream.QuestionInfo{
			Questions: normalized,
		})
	}

	return utils.InferTool("ask_user", desc, fn)
}

// normalizeQuestions 校验 agent 传的 questions 是否满足工具契约，同时给
// 缺失 ID 的题目补默认 id（q1/q2/...）。返回的切片是拷贝，不会改动传入的
// 原始参数。校验失败返回 err。
//
// 首次调用和恢复重跑都会走这一步，保证 renderAnswers 拿到的 questions.ID
// 跟前端 SSE frame 里给出、以及答案 payload 里回传的 QuestionID 完全一致，
// 否则 renderAnswers 按 ID 匹配会全部落到 "未回答" 分支。
func normalizeQuestions(raw []hitl.Question) ([]hitl.Question, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("questions 不能为空，至少要有 1 个 question")
	}
	if len(raw) > 5 {
		return nil, fmt.Errorf("questions 最多 5 个，收到 %d", len(raw))
	}
	out := make([]hitl.Question, len(raw))
	for i, q := range raw {
		if strings.TrimSpace(q.Question) == "" {
			return nil, fmt.Errorf("question[%d] 的 question 字段不能为空", i)
		}
		if len(q.Options) < 2 {
			return nil, fmt.Errorf("question[%d] options 至少 2 个，收到 %d", i, len(q.Options))
		}
		if len(q.Options) > 10 {
			return nil, fmt.Errorf("question[%d] options 最多 10 个，收到 %d；超过就该拆问题或者让用户自由输入", i, len(q.Options))
		}
		for j, opt := range q.Options {
			if strings.TrimSpace(opt) == "" {
				return nil, fmt.Errorf("question[%d] option[%d] 不能为空字符串", i, j)
			}
		}
		out[i] = q
		if out[i].ID == "" {
			out[i].ID = fmt.Sprintf("q%d", i+1)
		}
	}
	return out, nil
}

// renderAnswers 把用户答案拼成给 LLM 看的字符串。
//
// 单题：直接是选中的答案字符串。
// 多题：每题一行 "Q: 问题文本\nA: 答案"，最后一段是模型继续要用的原始上下文。
//
// 多选场景下多个 selected 用中文顿号连接；Other 自定义输入优先展示。
func renderAnswers(questions []hitl.Question, answers hitl.Answers) string {
	if len(questions) == 0 {
		return cancelledMessage
	}
	byID := map[string]hitl.Answer{}
	for _, a := range answers.Items {
		byID[a.QuestionID] = a
	}

	var sb strings.Builder
	single := len(questions) == 1
	for i, q := range questions {
		a := byID[q.ID]
		answerText := formatAnswerText(a)
		if answerText == "" {
			// 用户没答某题（多题场景可能出现）：标记出来让 LLM 知道
			answerText = "（未回答）"
		}
		if single {
			sb.WriteString(answerText)
			continue
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("Q%d: %s\nA%d: %s", i+1, q.Question, i+1, answerText))
	}
	return sb.String()
}

// formatAnswerText 单条 Answer 的显示文本。Other 输入优先，其次多选合并，
// 最后单选取首元素。
func formatAnswerText(a hitl.Answer) string {
	if strings.TrimSpace(a.Custom) != "" {
		return strings.TrimSpace(a.Custom)
	}
	filtered := make([]string, 0, len(a.Selected))
	for _, s := range a.Selected {
		if s = strings.TrimSpace(s); s != "" {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return strings.Join(filtered, "、")
}

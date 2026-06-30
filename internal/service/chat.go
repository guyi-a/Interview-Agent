package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ChatService struct {
	agent       *react.Agent
	manager     *stream.Manager
	convRepo    *repository.ConversationRepo
	msgRepo     *repository.MessageRepo
	projectRepo *repository.ProjectRepo
}

func NewChatService(
	ag *react.Agent,
	manager *stream.Manager,
	convRepo *repository.ConversationRepo,
	msgRepo *repository.MessageRepo,
	projectRepo *repository.ProjectRepo,
) *ChatService {
	return &ChatService{
		agent:       ag,
		manager:     manager,
		convRepo:    convRepo,
		msgRepo:     msgRepo,
		projectRepo: projectRepo,
	}
}

func (s *ChatService) Get(id string) *stream.StreamBuffer {
	return s.manager.Get(id)
}

func (s *ChatService) IsStreaming(id string) bool {
	return s.manager.IsStreaming(id)
}

func (s *ChatService) Cancel(id string) bool {
	buf := s.manager.Get(id)
	if buf == nil {
		return false
	}
	return buf.Cancel()
}

// Start begins (or continues) a chat turn:
//   - ensures conversation row exists
//   - if projectID is provided AND the conversation is new (or unbound), binds it
//   - loads prior messages as context
//   - persists the new user message
//   - kicks off the Agent in a goroutine, persists assistant reply when done
func (s *ChatService) Start(ctx context.Context, id, userMsg, projectID string) (*stream.StreamBuffer, error) {
	if err := s.convRepo.Upsert(ctx, id); err != nil {
		return nil, err
	}

	if projectID != "" {
		conv, err := s.convRepo.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		// Only bind when conversation has no project yet — silently ignore the
		// query param if it's already bound to a different (or same) project.
		if conv != nil && (conv.ProjectID == nil || *conv.ProjectID == "") {
			if err := s.convRepo.SetProjectID(ctx, id, projectID); err != nil {
				return nil, err
			}
		}
	}

	prior, err := s.msgRepo.List(ctx, id)
	if err != nil {
		return nil, err
	}

	history := toSchemaMessages(prior)
	if workspaceContext := s.workspaceContext(ctx, id); workspaceContext != "" {
		history = append([]*schema.Message{schema.SystemMessage(workspaceContext)}, history...)
	}
	history = append(history, schema.UserMessage(userMsg))

	if err := s.msgRepo.Append(ctx, &model.Message{
		ConversationID: id,
		Role:           string(schema.User),
		Content:        userMsg,
	}); err != nil {
		return nil, err
	}

	if title := truncateForTitle(userMsg); title != "" {
		_ = s.convRepo.SetTitleIfEmpty(ctx, id, title)
	}

	buf := s.manager.Create(id)
	runCtx, cancel := context.WithCancel(context.Background())
	buf.SetCancel(cancel)

	go s.runAgent(runCtx, id, history, buf)

	return buf, nil
}

func (s *ChatService) workspaceContext(ctx context.Context, convID string) string {
	if s.projectRepo == nil {
		return ""
	}
	conv, err := s.convRepo.Get(ctx, convID)
	if err != nil || conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		return "当前会话未绑定工作区。用户要求读写文件时，先调用 create_workspace 创建工作区。"
	}
	project, err := s.projectRepo.Get(ctx, *conv.ProjectID)
	if err != nil || project == nil {
		return "当前会话绑定的工作区记录不存在。用户要求读写文件时，先说明工作区不可用。"
	}
	return "当前会话已绑定工作区。project_id=" + project.ID + "，project_name=" + project.Name + "，workspace=" + project.Workspace + "。用户询问当前项目/工作区/文件时，直接调用 list_files/read_file/write_file/edit_file/mkdir 等文件工具，不要先询问是否已挂载工作区。"
}

func (s *ChatService) runAgent(ctx context.Context, convID string, msgs []*schema.Message, buf *stream.StreamBuffer) {
	ctx = contextkey.WithConversationID(ctx, convID)
	ctx = contextkey.WithBuffer(ctx, buf)

	collector := stream.NewRunCollector()
	cb := stream.NewSSEHandler(buf, collector)
	sr, err := s.agent.Stream(ctx, msgs,
		agent.WithComposeOptions(compose.WithCallbacks(cb)),
	)
	if err != nil {
		log.Printf("agent stream error: %v", err)
		stream.FinalizeErr(buf, err)
		return
	}
	defer sr.Close()

	for {
		_, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			collector.Wait()
			extra := ""
			if tools := collector.Tools(); len(tools) > 0 {
				if data, jerr := json.Marshal(map[string]any{"tools": tools}); jerr == nil {
					extra = string(data)
				} else {
					log.Printf("marshal tools: %v", jerr)
				}
			}
			if err := s.msgRepo.Append(context.Background(), &model.Message{
				ConversationID:   convID,
				Role:             string(schema.Assistant),
				Content:          collector.Content(),
				ReasoningContent: collector.Reasoning(),
				Extra:            extra,
			}); err != nil {
				log.Printf("persist assistant message: %v", err)
			}
			_ = s.convRepo.Upsert(context.Background(), convID)
			stream.FinalizeOK(buf)
			return
		}
		if err != nil {
			log.Printf("agent recv error: %v", err)
			stream.FinalizeErr(buf, err)
			return
		}
		// chunk discarded: the collector (via callback) is the source of truth
		// for persistence; we only drain here to keep the eino pipeline moving.
	}
}

func toSchemaMessages(rows []model.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, &schema.Message{
			Role:             schema.RoleType(r.Role),
			Content:          r.Content,
			ReasoningContent: r.ReasoningContent,
		})
	}
	return out
}

func truncateForTitle(s string) string {
	const maxRunes = 20
	runes := []rune(strings.TrimSpace(s))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "…"
}

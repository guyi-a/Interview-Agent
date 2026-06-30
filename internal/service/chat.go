package service

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ChatService struct {
	agent       *react.Agent
	manager     *stream.Manager
	convRepo    *repository.ConversationRepo
	msgRepo     *repository.MessageRepo
}

func NewChatService(
	ag *react.Agent,
	manager *stream.Manager,
	convRepo *repository.ConversationRepo,
	msgRepo *repository.MessageRepo,
) *ChatService {
	return &ChatService{
		agent:    ag,
		manager:  manager,
		convRepo: convRepo,
		msgRepo:  msgRepo,
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
//   - loads prior messages as context
//   - persists the new user message
//   - kicks off the Agent in a goroutine, persists assistant reply when done
func (s *ChatService) Start(ctx context.Context, id, userMsg string) (*stream.StreamBuffer, error) {
	if err := s.convRepo.Upsert(ctx, id); err != nil {
		return nil, err
	}

	prior, err := s.msgRepo.List(ctx, id)
	if err != nil {
		return nil, err
	}

	history := toSchemaMessages(prior)
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

func (s *ChatService) runAgent(ctx context.Context, convID string, msgs []*schema.Message, buf *stream.StreamBuffer) {
	cb := stream.NewSSEHandler(buf)
	sr, err := s.agent.Stream(ctx, msgs,
		agent.WithComposeOptions(compose.WithCallbacks(cb)),
	)
	if err != nil {
		log.Printf("agent stream error: %v", err)
		stream.FinalizeErr(buf, err)
		return
	}
	defer sr.Close()

	var content, reasoning strings.Builder
	for {
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			if err := s.msgRepo.Append(context.Background(), &model.Message{
				ConversationID:   convID,
				Role:             string(schema.Assistant),
				Content:          content.String(),
				ReasoningContent: reasoning.String(),
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
		if chunk == nil {
			continue
		}
		content.WriteString(chunk.Content)
		reasoning.WriteString(chunk.ReasoningContent)
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

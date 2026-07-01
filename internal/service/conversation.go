package service

import (
	"context"

	"github.com/guyi-a/Interview-Agent/internal/agent/browseruse"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ConversationService struct {
	convRepo   *repository.ConversationRepo
	msgRepo    *repository.MessageRepo
	manager    *stream.Manager
	browserMgr *browseruse.Manager
}

func NewConversationService(
	convRepo *repository.ConversationRepo,
	msgRepo *repository.MessageRepo,
	manager *stream.Manager,
	browserMgr *browseruse.Manager,
) *ConversationService {
	return &ConversationService{convRepo: convRepo, msgRepo: msgRepo, manager: manager, browserMgr: browserMgr}
}

func (s *ConversationService) List(ctx context.Context, limit int) ([]model.Conversation, error) {
	return s.convRepo.List(ctx, limit)
}

func (s *ConversationService) Messages(ctx context.Context, id string) ([]model.Message, error) {
	return s.msgRepo.List(ctx, id)
}

func (s *ConversationService) Delete(ctx context.Context, id string) error {
	if buf := s.manager.Get(id); buf != nil {
		buf.Cancel()
		s.manager.Remove(id)
	}
	if s.browserMgr != nil {
		s.browserMgr.CloseSession(id)
	}
	return s.convRepo.Delete(ctx, id)
}

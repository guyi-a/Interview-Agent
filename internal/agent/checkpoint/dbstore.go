// Package checkpoint adapts a repository-backed store to the eino
// adk.CheckPointStore interface so interrupted agent state survives across
// HTTP requests and process restarts.
package checkpoint

import (
	"context"

	"github.com/guyi-a/Interview-Agent/internal/repository"
)

type DBStore struct {
	repo *repository.CheckpointRepo
}

func NewDBStore(repo *repository.CheckpointRepo) *DBStore {
	return &DBStore{repo: repo}
}

func (s *DBStore) Set(ctx context.Context, id string, data []byte) error {
	return s.repo.Set(ctx, id, data)
}

func (s *DBStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	return s.repo.Get(ctx, id)
}

// Delete implements adk.CheckPointDeleter so eino cleans up finished runs.
func (s *DBStore) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

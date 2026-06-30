package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ProjectService struct {
	repo          *repository.ProjectRepo
	convRepo      *repository.ConversationRepo
	manager       *stream.Manager
	workspaceRoot string
}

func NewProjectService(
	repo *repository.ProjectRepo,
	convRepo *repository.ConversationRepo,
	manager *stream.Manager,
	workspaceRoot string,
) *ProjectService {
	return &ProjectService{
		repo:          repo,
		convRepo:      convRepo,
		manager:       manager,
		workspaceRoot: workspaceRoot,
	}
}

func (s *ProjectService) List(ctx context.Context) ([]model.Project, error) {
	return s.repo.List(ctx)
}

func (s *ProjectService) Get(ctx context.Context, id string) (*model.Project, error) {
	return s.repo.Get(ctx, id)
}

func (s *ProjectService) Rename(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	return s.repo.UpdateName(ctx, id, name)
}

// Delete removes the project + cascades conversations/messages + removes the
// workspace directory on disk. Streams under the project's conversations are
// cancelled first.
func (s *ProjectService) Delete(ctx context.Context, id string) error {
	project, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if project == nil {
		return nil
	}

	// Cancel any in-flight streams under this project before tearing down state.
	if s.manager != nil && s.convRepo != nil {
		if convs, err := s.convRepo.ListByProject(ctx, id); err == nil {
			for _, c := range convs {
				if buf := s.manager.Get(c.ID); buf != nil {
					buf.Cancel()
					s.manager.Remove(c.ID)
				}
			}
		}
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	// Filesystem cleanup. Safety: only delete if the dir is INSIDE workspaceRoot.
	if err := safeRemoveAll(s.workspaceRoot, project.Workspace); err != nil {
		// Don't fail the whole operation — DB is already gone. Log via returning
		// a wrapped error so the handler can surface it as a warning.
		return fmt.Errorf("project deleted, but workspace cleanup failed: %w", err)
	}
	return nil
}

// OpenInFinder opens the project's workspace directory in the OS file manager.
// macOS only for v1.
func (s *ProjectService) OpenInFinder(ctx context.Context, id string) error {
	p, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("project not found")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", p.Workspace)
	case "linux":
		cmd = exec.CommandContext(ctx, "xdg-open", p.Workspace)
	case "windows":
		cmd = exec.CommandContext(ctx, "explorer", p.Workspace)
	default:
		return fmt.Errorf("open is not supported on %s", runtime.GOOS)
	}
	return cmd.Start()
}

// safeRemoveAll removes dir, but only if it is strictly inside root.
func safeRemoveAll(root, dir string) error {
	if root == "" || dir == "" {
		return fmt.Errorf("empty path")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absDir)
	if err != nil {
		return err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove %q (outside workspace root %q)", absDir, absRoot)
	}
	return os.RemoveAll(absDir)
}

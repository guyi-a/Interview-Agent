package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

const (
	maxReadBytes  = 256 * 1024  // 256 KiB
	maxWriteBytes = 1024 * 1024 // 1 MiB
)

// fsDeps is the shared closure state for all fs tools.
type fsDeps struct {
	projectRepo *repository.ProjectRepo
	convRepo    *repository.ConversationRepo
}

// resolveWorkspace returns the absolute workspace path for the current
// conversation, or a user-readable error if no workspace is mounted yet
// (so the agent knows to call create_workspace first).
func (d *fsDeps) resolveWorkspace(ctx context.Context) (string, error) {
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return "", fmt.Errorf("internal error: no conversation in context")
	}
	conv, err := d.convRepo.Get(ctx, convID)
	if err != nil {
		return "", fmt.Errorf("load conversation: %w", err)
	}
	if conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		return "", fmt.Errorf("no workspace mounted for this conversation. Call create_workspace first")
	}
	project, err := d.projectRepo.Get(ctx, *conv.ProjectID)
	if err != nil {
		return "", fmt.Errorf("load project: %w", err)
	}
	if project == nil {
		return "", fmt.Errorf("project %q referenced by conversation no longer exists", *conv.ProjectID)
	}
	return project.Workspace, nil
}

// --- list_files ---

type ListFilesInput struct {
	Path string `json:"path" jsonschema:"description=Path to list. Relative to the workspace root (default '.' = workspace root)."`
}

type ListFilesEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  int64  `json:"size,omitempty"`
	IsDir bool   `json:"is_dir"`
}

type ListFilesOutput struct {
	Path    string           `json:"path"`
	Entries []ListFilesEntry `json:"entries"`
}

func newListFilesTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *ListFilesInput) (*ListFilesOutput, error) {
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		p := in.Path
		if p == "" {
			p = "."
		}
		abs, err := scope.Resolve(ws, p)
		if err != nil {
			return nil, err
		}
		dirents, err := os.ReadDir(abs)
		if err != nil {
			return nil, fmt.Errorf("read dir: %w", err)
		}
		out := &ListFilesOutput{Path: abs, Entries: make([]ListFilesEntry, 0, len(dirents))}
		for _, de := range dirents {
			info, err := de.Info()
			if err != nil {
				continue
			}
			entry := ListFilesEntry{Name: de.Name(), IsDir: de.IsDir()}
			if de.IsDir() {
				entry.Type = "dir"
			} else {
				entry.Type = "file"
				entry.Size = info.Size()
			}
			out.Entries = append(out.Entries, entry)
		}
		sort.Slice(out.Entries, func(i, j int) bool {
			a, b := out.Entries[i], out.Entries[j]
			if a.IsDir != b.IsDir {
				return a.IsDir
			}
			return a.Name < b.Name
		})
		return out, nil
	}
	return utils.InferTool(
		"list_files",
		"List directory contents inside the current workspace. Returns each entry with name, type ('file'/'dir') and size. Default path is the workspace root.",
		fn,
	)
}

// --- read_file ---

type ReadFileInput struct {
	Path string `json:"path" jsonschema:"description=File path to read. Relative to workspace root, or absolute inside workspace."`
}

type ReadFileOutput struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
}

func newReadFileTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *ReadFileInput) (*ReadFileOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		abs, err := scope.Resolve(ws, in.Path)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat: %w", err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("%q is a directory", in.Path)
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("open: %w", err)
		}
		defer f.Close()
		buf := make([]byte, maxReadBytes+1)
		n, err := f.Read(buf)
		if err != nil && n == 0 {
			return nil, fmt.Errorf("read: %w", err)
		}
		truncated := false
		if n > maxReadBytes {
			n = maxReadBytes
			truncated = true
		}
		return &ReadFileOutput{
			Path:      abs,
			Content:   string(buf[:n]),
			Truncated: truncated,
			SizeBytes: st.Size(),
		}, nil
	}
	return utils.InferTool(
		"read_file",
		fmt.Sprintf("Read a UTF-8 text file inside the workspace. Returns full content (truncated at %d KiB; check 'truncated').", maxReadBytes/1024),
		fn,
	)
}

// --- write_file ---

type WriteFileInput struct {
	Path    string `json:"path" jsonschema:"description=File path to write. Relative to workspace root. Parent directories are created automatically."`
	Content string `json:"content" jsonschema:"description=File content. UTF-8 text. The whole file is overwritten."`
}

type WriteFileOutput struct {
	Path      string `json:"path"`
	SizeBytes int    `json:"size_bytes"`
}

func newWriteFileTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *WriteFileInput) (*WriteFileOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		if len(in.Content) > maxWriteBytes {
			return nil, fmt.Errorf("content too large: %d bytes (max %d)", len(in.Content), maxWriteBytes)
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		abs, err := scope.Resolve(ws, in.Path)
		if err != nil {
			return nil, err
		}
		if abs == strings.TrimSuffix(ws, string(filepath.Separator)) {
			return nil, fmt.Errorf("refusing to write to the workspace root")
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir parent: %w", err)
		}
		if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
		return &WriteFileOutput{Path: abs, SizeBytes: len(in.Content)}, nil
	}
	return utils.InferTool(
		"write_file",
		"Create or fully overwrite a UTF-8 text file inside the workspace. Missing parent directories are created. Prefer edit_file for partial changes; use this only when creating a new file or rewriting the whole content.",
		fn,
	)
}

// --- edit_file ---

type EditFileInput struct {
	Path      string `json:"path" jsonschema:"description=File path to edit. Relative to workspace root."`
	OldString string `json:"old_string" jsonschema:"description=Exact text to find. Must appear EXACTLY ONCE in the file (otherwise the edit is rejected). Include enough surrounding context to disambiguate."`
	NewString string `json:"new_string" jsonschema:"description=Replacement text. Use empty string to delete the matched region."`
}

type EditFileOutput struct {
	Path           string `json:"path"`
	BytesBefore    int    `json:"bytes_before"`
	BytesAfter     int    `json:"bytes_after"`
	OccurrenceLine int    `json:"occurrence_line"` // 1-based line where the match started
}

func newEditFileTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *EditFileInput) (*EditFileOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		if in.OldString == "" {
			return nil, fmt.Errorf("old_string is required (use write_file to create or fully replace a file)")
		}
		if in.OldString == in.NewString {
			return nil, fmt.Errorf("old_string and new_string are identical; nothing to do")
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		abs, err := scope.Resolve(ws, in.Path)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}
		content := string(raw)
		count := strings.Count(content, in.OldString)
		if count == 0 {
			return nil, fmt.Errorf("old_string not found in %q", in.Path)
		}
		if count > 1 {
			return nil, fmt.Errorf("old_string matches %d locations in %q; add more surrounding context to make it unique", count, in.Path)
		}
		idx := strings.Index(content, in.OldString)
		line := 1 + strings.Count(content[:idx], "\n")
		out := content[:idx] + in.NewString + content[idx+len(in.OldString):]
		if len(out) > maxWriteBytes {
			return nil, fmt.Errorf("resulting file too large: %d bytes (max %d)", len(out), maxWriteBytes)
		}
		if err := os.WriteFile(abs, []byte(out), 0o644); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
		return &EditFileOutput{
			Path:           abs,
			BytesBefore:    len(raw),
			BytesAfter:     len(out),
			OccurrenceLine: line,
		}, nil
	}
	return utils.InferTool(
		"edit_file",
		"Make a targeted in-place edit by replacing one exact text occurrence with another. old_string must appear EXACTLY ONCE in the file — include enough surrounding context to make the match unique. Use empty new_string to delete. Preferred over write_file for partial changes.",
		fn,
	)
}

// --- mkdir ---

type MkdirInput struct {
	Path string `json:"path" jsonschema:"description=Directory path to create. Relative to workspace root. Intermediate directories are created automatically. No-op if already exists."`
}

type MkdirOutput struct {
	Path    string `json:"path"`
	Created bool   `json:"created"` // false if it already existed
}

func newMkdirTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *MkdirInput) (*MkdirOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		abs, err := scope.Resolve(ws, in.Path)
		if err != nil {
			return nil, err
		}
		existed := true
		if st, err := os.Stat(abs); err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("stat: %w", err)
			}
			existed = false
		} else if !st.IsDir() {
			return nil, fmt.Errorf("%q already exists and is not a directory", in.Path)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir: %w", err)
		}
		return &MkdirOutput{Path: abs, Created: !existed}, nil
	}
	return utils.InferTool(
		"mkdir",
		"Create a directory inside the workspace (mkdir -p semantics; no-op if it already exists). Use this before write_file when the desired parent layout doesn't exist yet.",
		fn,
	)
}

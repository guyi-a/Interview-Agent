package scope

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Resolve takes an absolute workspace root and a user-supplied path (which can
// be absolute OR relative to the workspace), normalizes it, and verifies the
// final absolute path stays inside the workspace.
//
// Returns the cleaned absolute path on success.
//
// This protects against:
//   - `..` traversal escaping the workspace
//   - Absolute paths pointing outside the workspace
//   - Symlinks are NOT resolved here (open-time os.Stat will follow them);
//     workspace owner is responsible for not creating malicious symlinks.
func Resolve(workspaceRoot, userPath string) (string, error) {
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root is empty")
	}
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("normalize workspace root: %w", err)
	}

	var target string
	if filepath.IsAbs(userPath) {
		target = userPath
	} else {
		target = filepath.Join(absRoot, userPath)
	}
	target = filepath.Clean(target)

	rel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", fmt.Errorf("resolve relative: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace %q", userPath, absRoot)
	}
	return target, nil
}

// ResolveRead is the read-side counterpart to Resolve.
//
// Semantics:
//   - Absolute userPath: cleaned and returned as-is. workspaceRoot is not
//     consulted; may be empty (relevant when the conversation has no
//     workspace bound but the caller wants to read a local file).
//   - Relative userPath: resolved against workspaceRoot (which must be
//     non-empty), and enforced to stay inside the root — same escape check
//     as Resolve.
//
// This is intentionally more permissive than Resolve on the absolute-path
// case: read tools trust the caller (single-user local machine), write tools
// keep the workspace fence.
func ResolveRead(workspaceRoot, userPath string) (string, error) {
	if userPath == "" {
		return "", fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(userPath) {
		return filepath.Clean(userPath), nil
	}
	if workspaceRoot == "" {
		return "", fmt.Errorf("relative path %q requires a workspace; pass an absolute path or bind a workspace first", userPath)
	}
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("normalize workspace root: %w", err)
	}
	target := filepath.Clean(filepath.Join(absRoot, userPath))
	rel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", fmt.Errorf("resolve relative: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace %q", userPath, absRoot)
	}
	return target, nil
}

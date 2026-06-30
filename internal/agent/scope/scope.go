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

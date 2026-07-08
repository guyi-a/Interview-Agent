package approval

import (
	"encoding/json"
	"strings"
)

// NeedsApproval decides whether a tool call must pause for human approval.
//
// v1 hardcoded rules (Sprint 11 P0):
//   - write_file, edit_file            → always
//   - write_file_chunked               → only when mode="start" (single approval
//     covers the whole append/finish sequence for that write)
//   - everything else                  → auto-approve
//
// The intent is to give the human a chance to say "no, don't overwrite this"
// before the agent commits changes. Read-only tools (read_file, list_files,
// file_info, extract_document_text) and workspace-only structural tools
// (mkdir, create_workspace) run without prompting.
func NeedsApproval(name, argsJSON string) bool {
	switch name {
	case "write_file", "edit_file", "edit_file_lines", "rm", "mv", "run_command":
		return true
	case "write_file_chunked":
		return chunkedMode(argsJSON) == "start"
	default:
		return false
	}
}

// chunkedMode pulls the "mode" field out of a chunked-write argument blob
// without decoding the whole thing. Returns "" if the mode is absent or the
// JSON is malformed — caller falls back to "approve every call", which is
// the conservative default.
func chunkedMode(argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var probe struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &probe); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(probe.Mode))
}

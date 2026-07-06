package approval

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// IsSafeAuto is the auto-mode fast path: rule-based recogniser for the
// obviously-boring subset of gated calls. Returning true skips both the LLM
// classifier and the human prompt.
//
// Scope today covers only the three write tools that NeedsApproval gates.
// A call qualifies as "safe auto" when every one of these holds:
//   - path is a plain relative path (no leading '/', '~', absolute Windows
//     drive, or ".." traversal)
//   - basename doesn't match a sensitive file pattern (.env, ssh keys,
//     cloud creds, .git/, etc.)
//   - content (for write_file / write_file_chunked) doesn't contain
//     credential signatures within the first 4 KiB
//
// Anything unrecognised returns false and falls through to the LLM
// classifier (or, if the classifier is disabled, human review). Callers
// should never treat a false as "unsafe" — it just means "we don't have
// a cheap deterministic answer".
//
// The reason string is for logs only, not for the model.
func IsSafeAuto(name, argsJSON string) (bool, string) {
	switch name {
	case "write_file", "edit_file":
		return isSafeWriteLike(argsJSON, /*hasContent=*/ name == "write_file")
	case "write_file_chunked":
		// chunked writes only reach approval on mode=start (see policy.go);
		// treat the first chunk's content like a normal write. mode!=start
		// shouldn't hit this middleware, but be defensive.
		if chunkedMode(argsJSON) != "start" {
			return false, "chunked non-start reached auto path"
		}
		return isSafeWriteLike(argsJSON, /*hasContent=*/ true)
	default:
		return false, "no fast-path rule for tool"
	}
}

func isSafeWriteLike(argsJSON string, hasContent bool) (bool, string) {
	var probe struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &probe); err != nil {
		return false, "unparseable args"
	}
	path := strings.TrimSpace(probe.Path)
	if path == "" {
		return false, "empty path"
	}
	if reason, ok := pathIsUnsafe(path); ok {
		return false, reason
	}
	if hasContent {
		if reason, ok := contentLooksSensitive(probe.Content); ok {
			return false, reason
		}
	}
	return true, "workspace_relative"
}

// pathIsUnsafe returns (reason, true) for paths the LLM should look at
// instead of the fast path silently approving them. Everything checked
// here is CHEAP — no filesystem access, purely string-level.
func pathIsUnsafe(path string) (string, bool) {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~") {
		return "absolute or home-prefixed path", true
	}
	// Windows-style absolute (C:\, D:\) — belt and braces even though we're
	// nominally macOS-only.
	if len(path) >= 2 && path[1] == ':' {
		return "windows absolute path", true
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "path traversal", true
	}
	// Even inside workspace, some basenames are load-bearing enough that we
	// don't want an auto-approve to slip past. Full segments (not substring
	// matches) so "docs/env-setup.md" isn't flagged.
	segments := strings.Split(cleaned, "/")
	for _, seg := range segments {
		if reason, hit := sensitiveSegment(seg); hit {
			return reason, true
		}
	}
	return "", false
}

var (
	// Exact-match basenames or family prefixes for common credential /
	// system-config files. Match runs on individual path segments, so we
	// use a small set of literals plus a couple of family predicates.
	sensitiveExactBasename = map[string]bool{
		".npmrc":     true,
		".pypirc":    true,
		".netrc":     true,
		".gitconfig": true,
	}
	// Directory names that should not be silently written into.
	sensitiveDirname = map[string]bool{
		".ssh":    true,
		".aws":    true,
		".gcp":    true,
		".azure":  true,
		".gnupg":  true,
		".config": true,
		".git":    true,
	}
	sshKeyPrefixRE = regexp.MustCompile(`^id_(rsa|ed25519|ecdsa|dsa)`)
)

func sensitiveSegment(seg string) (string, bool) {
	if seg == "" {
		return "", false
	}
	if sensitiveDirname[seg] {
		return "sensitive directory: " + seg, true
	}
	if sensitiveExactBasename[seg] {
		return "sensitive basename: " + seg, true
	}
	// .env, .env.local, .env.production, .env.example, ...
	if seg == ".env" || strings.HasPrefix(seg, ".env.") {
		return "dotenv file: " + seg, true
	}
	if sshKeyPrefixRE.MatchString(seg) {
		return "ssh key file: " + seg, true
	}
	return "", false
}

const contentScanCap = 4 * 1024

var credentialSignatureRE = regexp.MustCompile(
	`-----BEGIN ` + // PEM key blocks (RSA/EC/OPENSSH/CERTIFICATE)
		`|AKIA[0-9A-Z]{16}` + // AWS access key id
		`|ASIA[0-9A-Z]{16}` + // AWS temporary access key
		`|ghp_[A-Za-z0-9]{20,}` + // GitHub personal access token
		`|gho_[A-Za-z0-9]{20,}` + // GitHub OAuth token
		`|sk_live_[A-Za-z0-9]{16,}` + // Stripe live secret key
		`|xox[baprs]-[A-Za-z0-9-]{10,}`, // Slack tokens
)

func contentLooksSensitive(content string) (string, bool) {
	if content == "" {
		return "", false
	}
	head := content
	if len(head) > contentScanCap {
		head = head[:contentScanCap]
	}
	if m := credentialSignatureRE.FindString(head); m != "" {
		// Keep the reason short — a leaked key in the reason string would
		// end up in the log; store only the prefix classifier.
		return "credential signature in content", true
	}
	return "", false
}

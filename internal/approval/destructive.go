package approval

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// IsDestructive names tool calls that must always route to human approval,
// regardless of Mode — including full_access. The intent is a cross-cutting
// safety wall around irrecoverable operations (rm -rf, DROP TABLE, git
// reset --hard, etc.) so a user who elevated the mode can't accidentally
// green-light data loss with one click.
//
// 对 run_command 的判断走 shell AST 解析：用 mvdan/sh 把 command 字符串
// parse 成 AST，遍历所有 CallExpr（跨 && || ; | 分段自然拆开），对每一条
// 子命令做 pattern 匹配。parse 失败时保守返回 destructive（让 agent 明确
// 拿到一个可诊断的拦截）。
//
// Returns (isDestructive, humanReadableReason). The reason is logged, not
// shown to the model.
func IsDestructive(name, argsJSON string) (bool, string) {
	switch name {
	case "run_command":
		return checkShellCommand(argsJSON)
	default:
		return false, ""
	}
}

// checkShellCommand pulls the command out of run_command's args JSON and
// walks it. Absent / empty command → not destructive (regular arg validation
// in the tool will reject it). Malformed JSON → also not destructive here —
// InferTool's own binding layer will fail earlier with a cleaner error than
// what we can produce.
func checkShellCommand(argsJSON string) (bool, string) {
	if argsJSON == "" {
		return false, ""
	}
	var probe struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &probe); err != nil {
		return false, ""
	}
	cmd := strings.TrimSpace(probe.Command)
	if cmd == "" {
		return false, ""
	}
	return classifyShellCommand(cmd)
}

// classifyShellCommand parses the command line and returns (destructive,
// reason) on the first match. Parse failure itself is treated as destructive
// with reason "unparseable" — safer than letting an unusual quoting pattern
// slip past the wall.
func classifyShellCommand(cmd string) (bool, string) {
	file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return true, "shell parse failed: " + err.Error()
	}
	var (
		hit    bool
		reason string
	)
	syntax.Walk(file, func(node syntax.Node) bool {
		if hit {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		// call.Args[0] 是命令名 —— 但当前 CallExpr 也可能仅是变量赋值
		// (VAR=val)；只有出现真正的命令 token 时才判断。
		words := extractWords(call.Args)
		if len(words) == 0 {
			return true
		}
		if r, bad := matchDestructive(words); bad {
			hit = true
			reason = r
			return false
		}
		return true
	})
	if hit {
		return true, reason
	}
	return false, ""
}

// extractWords flattens the WordParts of each Word in a CallExpr into plain
// strings we can pattern-match. Literals + double-quoted literals are copied
// as-is; anything dynamic (subshell, param expansion, arithmetic) becomes an
// empty string — good enough for the coarse matching we do here. Bare
// assignments like FOO=bar prefixing a command show up as their own Word and
// are skipped (they contain '=' before any command token).
func extractWords(args []*syntax.Word) []string {
	out := make([]string, 0, len(args))
	for _, w := range args {
		s := literalOf(w)
		if s == "" {
			out = append(out, "")
			continue
		}
		// FOO=bar prefix — skip until we see a real command token.
		if len(out) == 0 && strings.ContainsRune(s, '=') && isEnvAssignment(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// literalOf pulls the concatenated literal string out of a Word. Non-literal
// parts (subshells, expansions) turn the whole word into "" — we can't safely
// match against something we can't see.
func literalOf(w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			// double-quoted: concat inner literals; give up on any dynamic bit.
			for _, inner := range p.Parts {
				if lit, ok := inner.(*syntax.Lit); ok {
					b.WriteString(lit.Value)
				} else {
					return ""
				}
			}
		default:
			return ""
		}
	}
	return b.String()
}

var envAssignRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func isEnvAssignment(s string) bool {
	return envAssignRE.MatchString(s)
}

// matchDestructive is the concrete pattern set. words[0] is the command; the
// rest are its arguments (literals only — see extractWords). Returns the
// human-readable reason on hit.
func matchDestructive(words []string) (string, bool) {
	cmd := filepath.Base(words[0])
	args := words[1:]

	switch cmd {
	case "sudo", "su", "doas":
		return "privilege escalation: " + cmd, true
	case "dd":
		// Only DDs that write to something are dangerous; `dd if=X` alone
		// (reading) is not. Look for of=.
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				return "dd of=... writes to a destination", true
			}
		}
	case "mkfs", "fdisk":
		return "disk-formatting tool: " + cmd, true
	case "diskutil":
		// diskutil erase*, secureErase, apfs eraseDisk, etc.
		for _, a := range args {
			la := strings.ToLower(a)
			if strings.HasPrefix(la, "erase") {
				return "diskutil " + a, true
			}
		}
	case "shred":
		return "shred permanently overwrites files", true
	case "rm":
		return matchRm(args)
	case "kill", "pkill", "killall":
		return "process-killing command: " + cmd, true
	case "chmod":
		return matchChmod(args)
	case "chown":
		return matchChown(args)
	case "truncate":
		// truncate -s 0 <file> — zeros out target files.
		for i, a := range args {
			if a == "-s" && i+1 < len(args) && strings.TrimLeft(args[i+1], "+") == "0" {
				return "truncate -s 0 zeros the target file", true
			}
			if a == "-s0" || a == "-s+0" {
				return "truncate -s0 zeros the target file", true
			}
		}
	case "git":
		return matchGit(args)
	}

	// mkfs.*, e.g. mkfs.ext4 / mkfs.apfs — filepath.Base loses the dot suffix
	// on the first switch only if the raw word already had a slash; fall
	// through here on the un-based name for safety.
	if strings.HasPrefix(cmd, "mkfs.") {
		return "disk-formatting tool: " + cmd, true
	}

	return "", false
}

// matchRm flags recursive removes and any rm targeting well-known dangerous
// paths (/, /tmp/**, $HOME/*, workspace root, etc.). Plain `rm foo.txt` is
// NOT flagged as destructive — it still gets caught by normal approval.
func matchRm(args []string) (string, bool) {
	recursive := false
	force := false
	targets := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			flags := a[1:]
			if strings.ContainsAny(flags, "rR") {
				recursive = true
			}
			if strings.ContainsRune(flags, 'f') {
				force = true
			}
			continue
		}
		switch a {
		case "--recursive", "--force":
			if a == "--recursive" {
				recursive = true
			} else {
				force = true
			}
			continue
		}
		targets = append(targets, a)
	}
	if recursive {
		if force {
			return "rm -rf", true
		}
		return "rm -r", true
	}
	for _, t := range targets {
		if isDangerousPathTarget(t) {
			return "rm targeting " + t, true
		}
	}
	return "", false
}

// matchChmod flags recursive world-writable / world-anything -R changes and
// the classic chmod 777.
func matchChmod(args []string) (string, bool) {
	recursive := false
	for _, a := range args {
		if a == "-R" || a == "--recursive" {
			recursive = true
			continue
		}
		if a == "777" || a == "0777" {
			if recursive {
				return "chmod -R 777", true
			}
			// Non-recursive 777 on one file is less scary; still flag it.
			return "chmod 777", true
		}
	}
	return "", false
}

// matchChown flags `chown -R` — recursive ownership changes are almost never
// what an agent should do on its own.
func matchChown(args []string) (string, bool) {
	for _, a := range args {
		if a == "-R" || a == "--recursive" {
			return "chown -R", true
		}
	}
	return "", false
}

// matchGit flags the small set of git operations that lose work: hard reset,
// clean -fd, force push. Regular commits / adds / status / diff are safe.
func matchGit(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	// Skip global flags like `-C dir` / `-c key=val` to find the subcommand.
	sub := ""
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-C" || a == "-c" {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "--") || strings.HasPrefix(a, "-") {
			i++
			continue
		}
		sub = a
		i++
		break
	}
	rest := args[i:]
	switch sub {
	case "reset":
		for _, a := range rest {
			if a == "--hard" {
				return "git reset --hard", true
			}
		}
	case "clean":
		for _, a := range rest {
			flags := strings.TrimPrefix(a, "-")
			if strings.HasPrefix(a, "-") && strings.ContainsRune(flags, 'f') &&
				(strings.ContainsRune(flags, 'd') || strings.ContainsRune(flags, 'x')) {
				return "git clean -fd/-fdx", true
			}
			if a == "--force" {
				return "git clean --force", true
			}
		}
	case "push":
		for _, a := range rest {
			if a == "--force" || a == "-f" || a == "--force-with-lease" {
				return "git push --force", true
			}
		}
	case "checkout", "restore":
		// git checkout . / git restore . 会丢工作区改动
		for _, a := range rest {
			if a == "." {
				return "git " + sub + " . discards working-tree changes", true
			}
		}
	}
	return "", false
}

// isDangerousPathTarget catches literal targets that shouldn't be blown
// away even with a bare rm: /, /*, /**, ~, $HOME, /tmp, /usr, /etc, etc.
// Purely string-level — the fast approval layer never touches the FS.
func isDangerousPathTarget(t string) bool {
	if t == "" {
		return false
	}
	// Strip a single leading -- (rm -- /path). We don't strip further to keep
	// paths recognisable in the reason string above.
	if t == "--" {
		return false
	}
	if t == "/" || t == "/*" || t == "/**" {
		return true
	}
	if t == "~" || t == "$HOME" || strings.HasPrefix(t, "~/") || strings.HasPrefix(t, "$HOME/") {
		return true
	}
	// Common system directories a coding agent has no business touching.
	dangerous := []string{
		"/bin", "/sbin", "/usr", "/etc", "/var", "/System", "/Library",
		"/Applications", "/private", "/boot", "/root", "/dev", "/proc",
	}
	for _, d := range dangerous {
		if t == d || strings.HasPrefix(t, d+"/") {
			return true
		}
	}
	return false
}

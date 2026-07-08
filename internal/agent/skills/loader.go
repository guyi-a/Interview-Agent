// Package skills provides a lazy-loaded skill registry.
//
// Each subdirectory of internal/agent/skills/ that contains a SKILL.md is a
// skill. SKILL.md must start with a minimal YAML frontmatter:
//
//	---
//	name: browser-use
//	description: <one-line trigger hints>
//	---
//	<markdown body>
//
// The system prompt only carries the (name, description) index — the body
// is fetched on demand by the load_skill tool.
//
// 除了 SKILL.md 之外，一个 skill 目录可以放辅助文件（REFERENCE.md / FORMS.md
// 之类），以及 scripts/ 子目录里放 agent 可直接执行的 python 脚本。整个 skill
// 目录在启动时释放到磁盘（默认 <dataDir>/skills/builtin/<name>/），agent 通过
// read_file / run_command 访问这些辅助文件和脚本。
package skills

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:*
var skillsFS embed.FS

type Skill struct {
	Name        string
	Description string
	Body        string
	// Path 是 skill 目录在磁盘上的绝对路径（<dataDir>/skills/builtin/<name>）。
	// agent 用这个路径去 read 辅助文件 或 run_command uv run <path>/scripts/xxx.py。
	Path string
}

type Loader struct {
	skills   map[string]Skill
	rootPath string // <dataDir>/skills/builtin
}

// NewLoader 释放 embed 内容到 dataDir/skills/builtin，然后扫每个子目录里的
// SKILL.md 建索引。每次启动会**清空** builtin 目录再重建，避免旧脚本残留。
// dataDir 为空时走当前工作目录下的 data/。
func NewLoader(dataDir string) (*Loader, error) {
	if dataDir == "" {
		dataDir = "data"
	}
	rootPath, err := filepath.Abs(filepath.Join(dataDir, "skills", "builtin"))
	if err != nil {
		return nil, fmt.Errorf("skills: resolve root: %w", err)
	}

	if err := os.RemoveAll(rootPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("skills: clean %s: %w", rootPath, err)
	}
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return nil, fmt.Errorf("skills: mkdir %s: %w", rootPath, err)
	}
	if err := extractEmbed(skillsFS, rootPath); err != nil {
		return nil, fmt.Errorf("skills: extract: %w", err)
	}

	out := map[string]Skill{}
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return nil, fmt.Errorf("skills: read root: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(rootPath, e.Name())
		mdPath := filepath.Join(skillDir, "SKILL.md")
		raw, err := os.ReadFile(mdPath)
		if err != nil {
			// 子目录里没有 SKILL.md 就跳过（不当作错误 —— 便于放非 skill 的辅助目录）
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("skills: read %s: %w", mdPath, err)
		}
		sk, err := parseSkill(string(raw))
		if err != nil {
			return nil, fmt.Errorf("skills: parse %s: %w", mdPath, err)
		}
		if sk.Name == "" {
			return nil, fmt.Errorf("skills: %s has empty name in frontmatter", mdPath)
		}
		if _, dup := out[sk.Name]; dup {
			return nil, fmt.Errorf("skills: duplicate name %q", sk.Name)
		}
		sk.Path = skillDir
		out[sk.Name] = sk
	}
	return &Loader{skills: out, rootPath: rootPath}, nil
}

// RootPath 返回 skill 目录在磁盘上的绝对路径。用于诊断和 UI 展示。
func (l *Loader) RootPath() string { return l.rootPath }

// Index returns the (name, description) list sorted alphabetically by name.
// Callers stitch it into the system prompt so the LLM knows what skills are
// available without paying the token cost of every body upfront.
func (l *Loader) Index() []Skill {
	names := make([]string, 0, len(l.skills))
	for n := range l.skills {
		names = append(names, n)
	}
	sortStrings(names)
	out := make([]Skill, len(names))
	for i, n := range names {
		s := l.skills[n]
		out[i] = Skill{Name: s.Name, Description: s.Description}
	}
	return out
}

// Load returns the full SKILL.md body for the given name (with Path filled).
func (l *Loader) Load(name string) (Skill, error) {
	s, ok := l.skills[name]
	if !ok {
		return Skill{}, ErrNotFound
	}
	return s, nil
}

// Names lists just the available skill names — used by the tool schema hint.
func (l *Loader) Names() []string {
	names := make([]string, 0, len(l.skills))
	for n := range l.skills {
		names = append(names, n)
	}
	sortStrings(names)
	return names
}

var ErrNotFound = errors.New("skill not found")

// extractEmbed 把 embed.FS 里的所有文件递归复制到 dst 目录。保留原目录结构。
// 权限：目录 0755，文件按扩展名决定 —— .py / .sh 给 0755（可执行），其他 0644。
func extractEmbed(src embed.FS, dst string) error {
	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		// 跳过 loader.go 本身（跟 SKILL.md 一起在 embed 根，但不是 skill 内容）
		if !d.IsDir() && filepath.Dir(path) == "." {
			return nil
		}
		target := filepath.Join(dst, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := src.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		mode := os.FileMode(0o644)
		switch strings.ToLower(filepath.Ext(path)) {
		case ".py", ".sh":
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// parseSkill splits a SKILL.md string into its YAML frontmatter and body.
// Only name and description keys are recognised; nothing fancier because
// the format is intentionally minimal.
func parseSkill(s string) (Skill, error) {
	if !strings.HasPrefix(s, "---\n") {
		return Skill{}, errors.New("missing frontmatter delimiter")
	}
	front, body, ok := strings.Cut(s[len("---\n"):], "\n---\n")
	if !ok {
		return Skill{}, errors.New("unterminated frontmatter")
	}

	sk := Skill{Body: body}
	for line := range strings.SplitSeq(front, "\n") {
		key, val, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "name":
			sk.Name = val
		case "description":
			sk.Description = val
		}
	}
	return sk, nil
}

// splitKV parses "key: value" (possibly with quotes around value). Ignores
// blank / comment lines.
func splitKV(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	k, v, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	k = strings.TrimSpace(k)
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		v = v[1 : len(v)-1]
	}
	return k, v, true
}

func sortStrings(s []string) {
	sort.Strings(s)
}

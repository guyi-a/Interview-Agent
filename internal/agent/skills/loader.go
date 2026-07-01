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
package skills

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed all:*/SKILL.md
var skillsFS embed.FS

type Skill struct {
	Name        string
	Description string
	Body        string
}

type Loader struct {
	skills map[string]Skill
}

// NewLoader scans the embedded skills directory once at boot and returns a
// registry keyed by frontmatter name.
func NewLoader() (*Loader, error) {
	entries, err := fs.ReadDir(skillsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("skills: read root: %w", err)
	}

	out := map[string]Skill{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mdPath := e.Name() + "/SKILL.md"
		raw, err := fs.ReadFile(skillsFS, mdPath)
		if err != nil {
			continue
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
		out[sk.Name] = sk
	}
	return &Loader{skills: out}, nil
}

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

// Load returns the full SKILL.md body for the given name.
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

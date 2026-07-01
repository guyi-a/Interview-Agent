package prompts

import (
	"strings"

	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
)

// WithSkillsIndex appends a "Skills 目录" section to base listing every
// available skill's name + description. The bodies stay off the prompt —
// the LLM pulls them on demand via load_skill(name).
//
// If loader is nil or has no skills, base is returned unchanged.
func WithSkillsIndex(base string, loader *skills.Loader) string {
	if loader == nil {
		return base
	}
	idx := loader.Index()
	if len(idx) == 0 {
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n## Skills 目录\n\n")
	b.WriteString("以下是可用的 skill —— 每个 skill 是一份特定任务的详细手册（工作流、纪律、失败处理等）。\n")
	b.WriteString("看到用户请求匹配下面某条描述，**立刻** `load_skill(name=...)` 拉完整手册，再按手册执行。不要凭感觉自己发挥。\n\n")
	for _, s := range idx {
		b.WriteString("- **")
		b.WriteString(s.Name)
		b.WriteString("** — ")
		b.WriteString(s.Description)
		b.WriteString("\n")
	}
	return b.String()
}

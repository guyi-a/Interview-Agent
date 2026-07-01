package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
)

type loadSkillInput struct {
	Name string `json:"name" jsonschema:"description=Skill name to load (must match one of the entries listed in the system prompt's Skills index)."`
}

type loadSkillOutput struct {
	OK      bool   `json:"ok"`
	Name    string `json:"name,omitempty"`
	Body    string `json:"body,omitempty"`
	Message string `json:"message,omitempty"`
}

func newLoadSkillTool(loader *skills.Loader) (tool.BaseTool, error) {
	if loader == nil {
		return nil, errors.New("load_skill: loader is nil")
	}
	names := loader.Names()
	desc := "Load the full instruction body of a skill listed in the system prompt's Skills index. " +
		"Call this the moment a user request matches a skill's trigger description — its body carries the exact " +
		"tool syntax, discipline, and failure recipes for that task. " +
		"Currently available: " + strings.Join(names, ", ") + "."
	return utils.InferTool("load_skill", desc, func(ctx context.Context, in *loadSkillInput) (*loadSkillOutput, error) {
		if in.Name == "" {
			return &loadSkillOutput{OK: false, Message: "name is required"}, nil
		}
		s, err := loader.Load(in.Name)
		if err != nil {
			return &loadSkillOutput{
				OK:      false,
				Message: fmt.Sprintf("skill %q not found; available: %s", in.Name, strings.Join(names, ", ")),
			}, nil
		}
		return &loadSkillOutput{OK: true, Name: s.Name, Body: s.Body}, nil
	})
}

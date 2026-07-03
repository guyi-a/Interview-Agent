package tools

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/browserbridge"
	"github.com/guyi-a/Interview-Agent/internal/agent/browseruse"
	"github.com/guyi-a/Interview-Agent/internal/agent/skills"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// Deps groups the dependencies the workspace + fs tools need at registration
// time. They are captured into each tool's closure.
type Deps struct {
	WorkspaceRoot    string
	ProjectRepo      *repository.ProjectRepo
	ConversationRepo *repository.ConversationRepo
	BrowserUseMgr    *browseruse.Manager
	BridgeService    *browserbridge.Service
	SkillLoader      *skills.Loader
}

// Builtin returns the full set of tools wired up with the given deps.
// Caller passes this slice to agent.NewReActAgent.
func Builtin(ctx context.Context, d Deps) ([]tool.BaseTool, error) {
	out := []tool.BaseTool{}

	timeTool, err := utils.InferTool(
		"get_current_time",
		"Get the current wall-clock time. USE THIS whenever the user asks about now/today/current time or the answer depends on the current moment — NEVER guess, and NEVER answer from memory; the model's own knowledge of the current time is unreliable.",
		currentTime,
	)
	if err != nil {
		return nil, err
	}
	out = append(out, timeTool)

	wsTool, err := NewCreateWorkspaceTool(d.WorkspaceRoot, d.ProjectRepo, d.ConversationRepo)
	if err != nil {
		return nil, err
	}
	out = append(out, wsTool)

	fs := &fsDeps{projectRepo: d.ProjectRepo, convRepo: d.ConversationRepo}
	for _, ctor := range []func(*fsDeps) (tool.BaseTool, error){
		newFileInfoTool,
		newListFilesTool,
		newReadFileTool,
		newExtractDocumentTextTool,
		newWriteFileTool,
		newChunkedWriteFileTool,
		newEditFileTool,
		newMkdirTool,
	} {
		t, err := ctor(fs)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}

	if d.BrowserUseMgr != nil {
		installTool, err := newBrowserUseInstallTool()
		if err != nil {
			return nil, err
		}
		bu, err := newBrowserUseTool(d.BrowserUseMgr, d.ConversationRepo, d.ProjectRepo)
		if err != nil {
			return nil, err
		}
		out = append(out, installTool, bu)
	}

	if d.BridgeService != nil {
		bb, err := newBrowserBridgeTool(d.BridgeService)
		if err != nil {
			return nil, err
		}
		out = append(out, bb)
	}

	if d.SkillLoader != nil {
		ls, err := newLoadSkillTool(d.SkillLoader)
		if err != nil {
			return nil, err
		}
		out = append(out, ls)
	}

	return out, nil
}

// --- get_current_time ---

type currentTimeInput struct {
	Timezone string `json:"timezone" jsonschema:"description=IANA timezone name like 'Asia/Shanghai' or 'UTC'. Empty for system local time."`
}

type currentTimeOutput struct {
	Time     string `json:"time"`
	Timezone string `json:"timezone"`
}

func currentTime(_ context.Context, in *currentTimeInput) (*currentTimeOutput, error) {
	loc := time.Local
	if in.Timezone != "" {
		if l, err := time.LoadLocation(in.Timezone); err == nil {
			loc = l
		}
	}
	now := time.Now().In(loc)
	return &currentTimeOutput{
		Time:     now.Format("2006-01-02 15:04:05"),
		Timezone: loc.String(),
	}, nil
}

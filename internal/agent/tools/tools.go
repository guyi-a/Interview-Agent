package tools

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

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

func Builtin(ctx context.Context) ([]tool.BaseTool, error) {
	t, err := utils.InferTool(
		"get_current_time",
		"Get the current wall-clock time. Use when the user asks what time/date it is, or when the answer depends on the current moment.",
		currentTime,
	)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{t}, nil
}

package main

import (
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		args     []string
		want     taskCommand
		errorHas string
	}{
		{[]string{"seo-pipeline", "task-1", "import"}, taskCommand{Name: "import"}, ""},
		{[]string{"seo-pipeline", "task_1", "import", "other.xlsx"}, taskCommand{Name: "import", ImportPath: "other.xlsx"}, ""},
		{[]string{"seo-pipeline", "task-1", "prepare", "37"}, taskCommand{Name: "prepare", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "generate", "37"}, taskCommand{Name: "generate", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "article", "37"}, taskCommand{Name: "article", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "info", "37"}, taskCommand{Name: "info", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "run", "37"}, taskCommand{Name: "run", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "run"}, taskCommand{Name: "run"}, ""},
		{[]string{"seo-pipeline", "task-1", "run", "--dry-run"}, taskCommand{Name: "run", DryRun: true}, ""},
		{[]string{"seo-pipeline", "--dry-run", "task_1", "run"}, taskCommand{Name: "run", DryRun: true}, ""},
		{[]string{"seo-pipeline", "task_1", "review", "37"}, taskCommand{Name: "review", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task_1", "fix", "37"}, taskCommand{Name: "fix", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task_1", "html", "37"}, taskCommand{Name: "html", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task_1"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "task_1", "unknown"}, taskCommand{}, `unknown task-1 operation "unknown"`},
		{[]string{"seo-pipeline", "task_1", "prepare"}, taskCommand{}, "usage"},
		{[]string{"seo-pipeline", "task_1", "generate", "not-a-number"}, taskCommand{}, "positive integer"},
		{[]string{"seo-pipeline", "article", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "info", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "review", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "fix", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "html", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "result", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "demo-generate", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "task-1", "result", "0"}, taskCommand{}, "positive integer"},
		{[]string{"seo-pipeline", "generate", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "task-1", "generate", "37", "--dry-run"}, taskCommand{}, "supported only"},
		{[]string{"seo-pipeline", "task-1", "run", "37", "--dry-run"}, taskCommand{}, "supported only"},
		{[]string{"seo-pipeline", "task-1", "run", "--dry-run", "--dry-run"}, taskCommand{}, "only once"},
	}
	for _, test := range tests {
		got, err := parseCommand(test.args)
		if test.errorHas != "" {
			if err == nil || !strings.Contains(err.Error(), test.errorHas) {
				t.Fatalf("parseCommand(%v) error = %v, want containing %q", test.args, err, test.errorHas)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("parseCommand(%v) = %+v, %v; want %+v", test.args, got, err, test.want)
		}
	}
}

func TestUseGeminiModelAppliesEnvironmentModelToEveryStage(t *testing.T) {
	cfg := config.LLMConfig{Stages: map[string]config.LLMStageConfig{
		"article": {Provider: "gemini", Model: "yaml-model"},
		"info":    {Provider: "gemini", Model: "yaml-model"},
	}}
	if err := useGeminiModel(&cfg, "gemini-from-env"); err != nil {
		t.Fatal(err)
	}
	for name, stage := range cfg.Stages {
		if stage.Model != "gemini-from-env" {
			t.Fatalf("%s model = %q", name, stage.Model)
		}
	}
}

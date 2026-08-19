package main

import (
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof2"
)

// Схема стадий pprof_2 обязана собираться и вести только к DeepSeek: второй схемы у задачи
// нет, и выбирать между ними не из чего.
func TestPProf2HasSingleDeepSeekScheme(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("LLM_MODE", "")

	configs, err := loadStageConfigs(mustProfile(pprof2.Command), modeTestLogger(), true)
	if err != nil {
		t.Fatal(err)
	}
	if configs.geminiFound {
		t.Fatal("у pprof_2 не должно быть схемы Gemini")
	}
	for stage, settings := range configs.deepseek.Stages {
		for _, target := range settings.Targets {
			if target.Provider != "deepseek_web" {
				t.Fatalf("стадия %s идёт к провайдеру %q, а pprof_2 DeepSeek-only", stage, target.Provider)
			}
		}
	}
	for _, stage := range pprof2.Stages {
		if _, found := configs.deepseek.Stages[stage]; !found {
			t.Fatalf("стадия %s объявлена в профиле, но отсутствует в схеме", stage)
		}
	}
	if _, found := configs.deepseek.Stages["info"]; found {
		t.Fatal("в схеме pprof_2 есть стадия info, хотя задача её не выполняет")
	}
}

// Основной промпт — автор страницы, и собирается он из входных данных и research. Потеря
// любого плейсхолдера означает промпт без ключей, без LSI или без структуры.
func TestPProf2MainPromptKeepsItsPlaceholders(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("LLM_MODE", "")

	configs, err := loadStageConfigs(mustProfile(pprof2.Command), modeTestLogger(), true)
	if err != nil {
		t.Fatal(err)
	}
	stage, found := configs.deepseek.Stages[pprof2.StageArticle]
	if !found {
		t.Fatal("стадия основного промпта отсутствует в схеме pprof_2")
	}
	for _, placeholder := range []string{"{{.Title}}", "{{.Keywords}}", "{{.LSIWords}}",
		"{{.GeneratedStructure}}"} {
		if !strings.Contains(stage.PromptTemplate, placeholder) {
			t.Fatalf("основной промпт потерял %s", placeholder)
		}
	}
}

// Промпты задач не пересекаются: правка одной не имеет права менять другую.
func TestPProf2PromptsAreItsOwn(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("LLM_MODE", "")

	own, err := loadStageConfigs(mustProfile(pprof2.Command), modeTestLogger(), true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := loadStageConfigs(mustProfile("pprof-1"), modeTestLogger(), true)
	if err != nil {
		t.Fatal(err)
	}
	for stage, settings := range own.deepseek.Stages {
		foreign, found := other.deepseek.Stages[stage]
		if !found {
			continue
		}
		if strings.TrimSpace(settings.PromptTemplate) == strings.TrimSpace(foreign.PromptTemplate) {
			t.Fatalf("промпт стадии %s совпадает с pprof_1: задачи делят один файл", stage)
		}
	}
}

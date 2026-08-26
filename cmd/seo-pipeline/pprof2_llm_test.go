package main

import (
	"strings"
	"testing"
	"text/template"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
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

// DEMO рендерит те же шаблоны, что и боевой прогон, а поля для них собирает задача. Разойдись
// наборы — сборка падала бы на рендере основного промпта, уже после оплаченной структуры,
// и папка выходила бы без промпта и без текста страницы.
func TestPProf2DemoPromptDataRendersItsRealPrompts(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("LLM_MODE", "")

	configs, err := loadStageConfigs(mustProfile(pprof2.Command), modeTestLogger(), true)
	if err != nil {
		t.Fatal(err)
	}
	promptData := demoPromptDataOf(pprof2.NewFlow(nil, nil, nil, nil, nil, nil))
	if promptData == nil {
		t.Fatal("pprof_2 не отдал DEMO поля своих промптов")
	}
	input := article.GenerationInput{
		Article:             article.Article{ID: 4, ExternalID: "4", Title: "Онкология", Slug: "onkologiya"},
		CompetitorStructure: "H2 О программе",
		WordstatKeywords:    []article.KeywordFrequency{{Query: "онкология обучение", Frequency: 880}},
		LSIWords:            []string{"диспансеризация"},
		Teachers:            "Иванова И. И., врач-онколог",
	}
	for _, stage := range []struct {
		name string
		data any
	}{
		{pprof2.StageStructure, promptData.StructureData(input)},
		{pprof2.StageArticle, promptData.ArticleData(input, "СГЕНЕРИРОВАННАЯ СТРУКТУРА")},
	} {
		settings, found := configs.deepseek.Stages[stage.name]
		if !found {
			t.Fatalf("стадия %s отсутствует в схеме pprof_2", stage.name)
		}
		prompt, parseErr := template.New(stage.name).Parse(settings.PromptTemplate)
		if parseErr != nil {
			t.Fatalf("промпт стадии %s не разобран: %v", stage.name, parseErr)
		}
		var rendered strings.Builder
		if execErr := prompt.Execute(&rendered, stage.data); execErr != nil {
			t.Fatalf("промпт стадии %s не собрался полями задачи: %v", stage.name, execErr)
		}
		if strings.Contains(rendered.String(), "<no value>") {
			t.Fatalf("в промпте стадии %s осталось незаполненное поле:\n%s", stage.name, rendered.String())
		}
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

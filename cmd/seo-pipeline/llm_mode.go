package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/generation"
)

const (
	baseLLMConfigPath     = "config/config.yaml"
	deepseekLLMConfigPath = "config/config.deepseek.yaml"
)

// stageConfigs — обе схемы стадий сразу.
//
// DeepSeek-only грузится всегда: он не требует ключа Gemini и служит запасным режимом.
// Схема Gemini может отсутствовать — тогда работаем только через DeepSeek.
type stageConfigs struct {
	gemini      config.LLMConfig
	geminiFound bool
	deepseek    config.LLMConfig
	// geminiAbsenceReason объясняет отсутствие схемы Gemini в отчёте и в логе.
	geminiAbsenceReason string
}

// providers объединяет описания провайдеров обеих схем.
//
// Брать их из одной схемы нельзя: это работает только пока deepseek-схема остаётся базовым
// файлом плюс overlay. Схема, объявившая своего провайдера, молча получила бы пустой тип.
func (c stageConfigs) providers() map[string]config.LLMProviderConfig {
	merged := make(map[string]config.LLMProviderConfig, len(c.deepseek.Providers))
	for _, stageSet := range c.stageSets() {
		for name, provider := range stageSet.Providers {
			merged[name] = provider
		}
	}
	return merged
}

// models возвращает первую непустую модель каждого провайдера по обеим схемам.
//
// Искать в одной схеме нельзя: DeepSeek-only не упоминает gemini вовсе, и поиск в ней
// возвращал пустую модель, из-за чего создание клиента падало с «GEMINI_MODEL is empty»
// ещё до того, как выяснялось, что Gemini в этом прогоне не нужен.
func (c stageConfigs) models() map[string]string {
	models := make(map[string]string)
	for _, stageSet := range c.stageSets() {
		for _, stage := range stageSet.Stages {
			for _, target := range stage.Targets {
				if models[target.Provider] == "" {
					models[target.Provider] = strings.TrimSpace(target.Model)
				}
			}
		}
	}
	return models
}

func (c stageConfigs) stageSets() []config.LLMConfig {
	if c.geminiFound {
		return []config.LLMConfig{c.deepseek, c.gemini}
	}
	return []config.LLMConfig{c.deepseek}
}

// loadStageConfigs готовит обе схемы. LLM_MODE=deepseek жёстко фиксирует DeepSeek-only,
// пустое значение или gemini включают автоматический выбор режима на каждую статью.
//
// requireCredentials выключается проверкой перед дорогим прогоном: ей нужна схема Gemini даже
// там, где ключа нет, — чтобы сказать «Gemini недоступен, потому что ...», а не упасть.
// Доступность при этом решает не загрузка схемы, а llmResolver.
func loadStageConfigs(logger *slog.Logger, requireCredentials bool) (stageConfigs, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_MODE")))
	switch mode {
	case "", "gemini", "deepseek":
	default:
		return stageConfigs{}, fmt.Errorf("неподдерживаемый LLM_MODE %q; допустимо: gemini, deepseek", mode)
	}

	deepseek, err := config.LoadLLMConfigWithOverlay(baseLLMConfigPath, deepseekLLMConfigPath, requireCredentials)
	if err != nil {
		return stageConfigs{}, err
	}
	configs := stageConfigs{deepseek: deepseek}
	if mode == "deepseek" {
		configs.geminiAbsenceReason = "LLM_MODE=deepseek"
		logger.Info("LLM mode is pinned to DeepSeek-only", "config", deepseekLLMConfigPath, "reason", configs.geminiAbsenceReason)
		return configs, nil
	}

	gemini, err := loadLLMScheme(baseLLMConfigPath, requireCredentials)
	if err != nil {
		// Отсутствие ключа или сломанная схема Gemini не должны останавливать работу:
		// DeepSeek-only остаётся рабочим режимом.
		configs.geminiAbsenceReason = "схема Gemini не загрузилась: " + err.Error()
		logger.Warn("Gemini scheme is unavailable, every article will use DeepSeek-only", "error", err)
		return configs, nil
	}
	configs.gemini, configs.geminiFound = gemini, true
	return configs, nil
}

func loadLLMScheme(path string, requireCredentials bool) (config.LLMConfig, error) {
	if requireCredentials {
		return config.LoadLLMConfig(path)
	}
	return config.LoadLLMConfigForDryRun(path)
}

// articleMode выбирает схему на статью и следит за доступностью Gemini.
//
// Режим определяется один раз в начале статьи и внутри неё не меняется: перед стадиями
// ничего не проверяется и пробных запросов к Gemini не делается. Доступность выясняется
// только по реальному ответу модели.
type articleMode struct {
	pipelines    map[schemeName]*generation.Pipeline
	resolver     llmResolver
	availability geminiAvailability
	logger       *slog.Logger
	// restart переделывает статью целиком после переключения режима. Поле нужно, чтобы
	// проверять переключение без настоящих конвейеров; по умолчанию — полный прогон.
	restart func(context.Context, *generation.Pipeline, string) error
}

// pipelineFor возвращает схему для новой статьи и объясняет выбор в логе.
//
// Возвращается имя схемы, а не только конвейер: сравнивать выбранную схему по адресу объекта
// значит держать состояние перехода в структуре вызовов, а не в данных.
func (m *articleMode) pipelineFor(externalID string) (schemeName, *generation.Pipeline) {
	routing := m.resolver.Resolve()
	pipeline, found := m.pipelines[routing.Scheme]
	if !found {
		// Клиент нужной схемы не создан при старте — например, маркер выключения Gemini
		// истёк уже во время прогона. Молча подменять схему нельзя: в логе останется след.
		m.logger.Warn("scheme is unavailable in this process, falling back to DeepSeek-only",
			"external_id", externalID, "requested_scheme", string(routing.Scheme),
			"reason", routing.Reason, "mode", string(schemeDeepSeek))
		return schemeDeepSeek, m.pipelines[schemeDeepSeek]
	}
	m.logger.Info("article scheme selected",
		"external_id", externalID, "mode", string(routing.Scheme), "reason", routing.Reason)
	return routing.Scheme, pipeline
}

// run прогоняет статью выбранной схемой и передаёт результат в guard.
func (m *articleMode) run(ctx context.Context, externalID string, runOne func(context.Context, *generation.Pipeline, string) error) error {
	scheme, selected := m.pipelineFor(externalID)
	return m.guard(ctx, externalID, scheme, runOne(ctx, selected, externalID))
}

// stage выполняет одну стадию выбранной схемой.
//
// Переключения режима здесь намеренно нет: перезапускать всю статью ради одной стадии
// бессмысленно.
func (m *articleMode) stage(ctx context.Context, externalID string, run func(context.Context, *generation.Pipeline, string) error) error {
	_, selected := m.pipelineFor(externalID)
	return run(ctx, selected, externalID)
}

// guard решает, что делать с результатом прогона статьи.
//
// Если схема Gemini упёрлась в квоту или оплату, Gemini выключается на сутки, а статья
// переделывается целиком через DeepSeek-only. Смешанного режима внутри статьи не возникает:
// переключение всегда сопровождается полным перезапуском генерации.
//
// Вынесено отдельно от run, потому что команды run и regenerate идут через постадийный
// возобновляемый раннер и не укладываются в одну функцию-прогон.
func (m *articleMode) guard(ctx context.Context, externalID string, scheme schemeName, err error) error {
	if err == nil || scheme != schemeGemini || !isGeminiExhausted(err) {
		return err
	}
	until, saveErr := m.availability.disable(exhaustionReason(err))
	if saveErr != nil {
		m.logger.Error("Gemini state was not saved, the next run may try Gemini again", "error", saveErr)
	}
	m.logger.Warn("Gemini is disabled, the article restarts from scratch through DeepSeek-only",
		"external_id", externalID, "reason", exhaustionReason(err),
		"gemini_disabled_until", until.UTC().Format(time.RFC3339), "ttl", geminiUnavailableTTL.String(),
		"mode", string(schemeDeepSeek), "error", err)
	// Полный путь генерации: он начинается с BeginGeneration и переделывает все стадии.
	restart := m.restart
	if restart == nil {
		restart = runGenerate
	}
	return restart(ctx, m.pipelines[schemeDeepSeek], externalID)
}

// isGeminiExhausted опознаёт исчерпание квоты или оплаты по типу ошибки, а не по тексту.
// Браузерный провайдер этих типов не возвращает, поэтому спутать режимы нельзя.
func isGeminiExhausted(err error) bool {
	var statusErr *llm.StatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.Type == llm.ErrorTypeQuotaExhausted || statusErr.Type == llm.ErrorTypeCreditsExhausted
}

func exhaustionReason(err error) string {
	var statusErr *llm.StatusError
	if errors.As(err, &statusErr) {
		return string(statusErr.Type)
	}
	return "unknown"
}

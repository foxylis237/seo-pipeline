package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/llm/deepseekweb"
	llmgemini "github.com/foxylis237/seo-pipeline/internal/llm/gemini"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/generation"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/keywords"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/repository"
	resultassembly "github.com/foxylis237/seo-pipeline/internal/tasks/task1/result"
)

// schemeName — имя схемы стадий. Схема, а не провайдер: gemini-схема сама смешивает
// провайдеров (structure, info и html идут в DeepSeek), поэтому «схема Gemini» не значит
// «всё через Gemini».
type schemeName string

const (
	schemeGemini   schemeName = "gemini"
	schemeDeepSeek schemeName = "deepseek_only"
)

// resolvedRouting — итог выбора маршрутизации: одна схема стадий и объяснение, почему она.
//
// Это единственный ответ на вопрос «чем пойдёт статья». Ни роутер, ни конвейер его не
// пересматривают: роутер читает только Config.Stages[...].Targets, конвейер про существование
// второй схемы вообще не знает.
type resolvedRouting struct {
	Scheme    schemeName
	Reason    string
	Config    config.LLMConfig
	Providers []providerStatus
}

// providerStatus — доступность провайдера по локальным признакам. Сетевых запросов и запуска
// браузера здесь нет: проверка перед дорогим прогоном не должна сама стоить денег или бана.
type providerStatus struct {
	Name      string
	Type      string
	Available bool
	Detail    string
}

// llmResolver держит обе схемы и состояние доступности Gemini.
type llmResolver struct {
	configs      stageConfigs
	availability geminiAvailability
	now          func() time.Time
}

func newLLMResolver(configs stageConfigs, availability geminiAvailability) llmResolver {
	return llmResolver{configs: configs, availability: availability, now: time.Now}
}

// Resolve выбирает схему. Вызывается на каждую статью, но решение всегда принимается здесь и
// только здесь: раньше маркер доступности читали независимо main и выбор схемы, и две
// политики уже успели разойтись.
func (r llmResolver) Resolve() resolvedRouting {
	now := r.now()
	statuses := r.providerStatuses(now)
	routing := resolvedRouting{Providers: statuses}

	switch {
	case !r.configs.geminiFound:
		routing.Scheme, routing.Reason = schemeDeepSeek, r.configs.geminiAbsenceReason
	default:
		until, reason, blocked, _ := r.availability.state()
		if blocked {
			routing.Scheme = schemeDeepSeek
			routing.Reason = fmt.Sprintf("Gemini выключен до %s (%s)", until.UTC().Format(time.RFC3339), reason)
			break
		}
		if status, found := findProviderStatus(statuses, "gemini"); found && !status.Available {
			routing.Scheme, routing.Reason = schemeDeepSeek, status.Detail
			break
		}
		routing.Scheme, routing.Reason = schemeGemini, "Gemini доступен"
	}

	if routing.Scheme == schemeGemini {
		routing.Config = r.configs.gemini
	} else {
		routing.Config = r.configs.deepseek
	}
	return routing
}

func findProviderStatus(statuses []providerStatus, name string) (providerStatus, bool) {
	for _, status := range statuses {
		if status.Name == name {
			return status, true
		}
	}
	return providerStatus{}, false
}

// providerStatuses описывает каждого провайдера, упомянутого хотя бы одной схемой.
func (r llmResolver) providerStatuses(now time.Time) []providerStatus {
	providers := r.configs.providers()
	models := r.configs.models()
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)

	statuses := make([]providerStatus, 0, len(names))
	for _, name := range names {
		statuses = append(statuses, r.providerStatus(name, providers[name], models[name], now))
	}
	return statuses
}

func (r llmResolver) providerStatus(name string, provider config.LLMProviderConfig, model string, now time.Time) providerStatus {
	status := providerStatus{Name: name, Type: provider.Type, Available: true}
	switch provider.Type {
	case "deepseek_web":
		if _, err := os.Stat(provider.ProfileDir); err != nil {
			return failedStatus(status, fmt.Sprintf("профиль %s отсутствует — нужен вход через deepseek-login", provider.ProfileDir))
		}
		if until, reason, blocked := deepseekweb.BlockedUntil(provider.ProfileDir, now); blocked {
			detail := fmt.Sprintf("аккаунт недоступен до %s", until.UTC().Format(time.RFC3339))
			if reason != "" {
				detail += " (" + reason + ")"
			}
			return failedStatus(status, detail)
		}
		status.Detail = "профиль " + provider.ProfileDir + " готов"
	default:
		if strings.TrimSpace(os.Getenv(provider.APIKeyEnv)) == "" {
			return failedStatus(status, provider.APIKeyEnv+" не задан")
		}
		if strings.TrimSpace(model) == "" {
			return failedStatus(status, "модель не задана в окружении")
		}
		status.Detail = provider.APIKeyEnv + " задан, модель " + model
	}
	if name == "gemini" {
		if until, reason, blocked, _ := r.availability.state(); blocked {
			detail := fmt.Sprintf("выключен до %s", until.UTC().Format(time.RFC3339))
			if reason != "" {
				detail += " (" + reason + ")"
			}
			return failedStatus(status, detail)
		}
	}
	return status
}

func failedStatus(status providerStatus, detail string) providerStatus {
	status.Available, status.Detail = false, detail
	return status
}

// generationDeps — зависимости конвейера, одинаковые для обеих схем.
type generationDeps struct {
	repository *repository.ArticleRepository
	writer     *articleoutput.Writer
	result     *resultassembly.Service
	logger     *slog.Logger
	// promptPublisher выгружает готовый промпт статьи наружу. Необязателен: у prepare и
	// dry-run его нет, и конвейер тогда работает ровно как раньше.
	promptPublisher generation.PromptPublisher
}

// buildLLM создаёт клиентов и конвейеры под разрешённую маршрутизацию.
//
// Возвращается закрыватель, а не срез клиентов: вызывающему нужно ровно одно — закрыть всё
// однажды, и он не должен помнить, у кого из клиентов есть Close.
func buildLLM(ctx context.Context, configs stageConfigs, availability geminiAvailability, deps generationDeps) (*articleMode, func() error, error) {
	resolver := newLLMResolver(configs, availability)
	routing := resolver.Resolve()
	deps.logger.Info("LLM routing resolved", "mode", string(routing.Scheme), "reason", routing.Reason)

	// Схема DeepSeek-only нужна всегда: на неё уходит статья после исчерпания квоты Gemini.
	// Клиенты общие для схем — браузерный профиль один, и второй экземпляр не получил бы flock.
	schemes := map[schemeName]config.LLMConfig{schemeDeepSeek: configs.deepseek}
	if routing.Scheme == schemeGemini {
		schemes[schemeGemini] = configs.gemini
	}

	providers := configs.providers()
	models := configs.models()
	used := make(map[string]struct{})
	for _, stageSet := range schemes {
		for _, stage := range stageSet.Stages {
			for _, target := range stage.Targets {
				used[target.Provider] = struct{}{}
			}
		}
	}

	clients := make(map[string]llm.Client, len(used))
	var closers []func() error
	closeAll := func() error {
		var errs []error
		for _, close := range closers {
			if err := close(); err != nil {
				errs = append(errs, fmt.Errorf("закрыть LLM client: %w", err))
			}
		}
		return errors.Join(errs...)
	}
	for _, name := range sortedKeys(used) {
		client, closer, err := newLLMClient(ctx, name, providers[name], models[name], deps.logger)
		if err != nil {
			return nil, closeAll, fmt.Errorf("создать LLM provider %q: %w", name, err)
		}
		clients[name] = client
		if closer != nil {
			closers = append(closers, closer)
		}
	}

	// review и fix идут одним чатом: fix опирается на историю, а не на повторную передачу
	// статьи и ревью.
	pipelines := make(map[schemeName]*generation.Pipeline, len(schemes))
	routers := make(map[schemeName]*llm.Router, len(schemes))
	for name, stageSet := range schemes {
		router := llm.NewRouter(stageSet, clients, deps.logger)
		routers[name] = router
		pipeline := generation.NewPipeline(deps.repository, router, router.NewStageChatFactory("review", "fix"),
			deps.writer, deps.logger, deps.result)
		// Публикация подключается обеим схемам: статья, переехавшая на DeepSeek после
		// исчерпания квоты Gemini, выгружает промпт так же, как обычная.
		if deps.promptPublisher != nil {
			pipeline.SetPromptPublisher(deps.promptPublisher)
		}
		pipelines[name] = pipeline
	}
	return &articleMode{
		pipelines: pipelines, routers: routers, resolver: resolver, availability: availability, logger: deps.logger,
	}, closeAll, nil
}

// newKeywordsFallbackFactory строит резервный источник исходных запросов для prepare.
//
// Схему выбирает тот же резолвер, что и генерация: у prepare не должно быть своей политики
// маршрутизации. Нужную стадию (keywords) роутер находит в той же конфигурации.
func newKeywordsFallbackFactory(mode *articleMode, logger *slog.Logger) keywordsFallbackFactory {
	if mode == nil {
		return nil
	}
	return func(selected article.Article) keywordsFallback {
		return keywords.NewFallback(mode.routerFor(selected.ExternalID), selected.ID, logger)
	}
}

// newPrepareKeywordsFallback готовит резервный источник запросов для команды prepare.
//
// Неудача сборки LLM не роняет prepare: команда и раньше обходилась без модели, а резерв
// нужен ровно в том редком случае, когда Keys.so ничего не нашёл. Поэтому здесь
// предупреждение и прогон без резерва, а не отказ команды.
func newPrepareKeywordsFallback(ctx context.Context, deps generationDeps) (keywordsFallbackFactory, func() error) {
	noop := func() error { return nil }
	stages, err := loadStageConfigs(deps.logger, true)
	if err != nil {
		deps.logger.Warn("резервный подбор запросов недоступен: LLM-конфигурация не загрузилась",
			"stage", keywords.StageName, "error", err)
		return nil, noop
	}
	mode, closeLLM, err := buildLLM(ctx, stages, newGeminiAvailability(), deps)
	if err != nil {
		deps.logger.Warn("резервный подбор запросов недоступен: LLM-провайдеры не созданы",
			"stage", keywords.StageName, "error", err)
		if closeLLM == nil {
			return nil, noop
		}
		return nil, closeLLM
	}
	return newKeywordsFallbackFactory(mode, deps.logger), closeLLM
}

// newLLMClient конструирует одного провайдера. Ветка default обязательна: новый тип, добавленный
// в normalizeLLMProvider и забытый здесь, иначе давал бы не ошибку старта, а «provider is not
// registered» в середине прогона.
func newLLMClient(ctx context.Context, name string, provider config.LLMProviderConfig, model string, logger *slog.Logger) (llm.Client, func() error, error) {
	switch provider.Type {
	case "gemini":
		client, err := llmgemini.NewClient(ctx, os.Getenv(provider.APIKeyEnv), model)
		if err != nil {
			return nil, nil, err
		}
		return client, client.Close, nil
	case "openai_compatible":
		client, err := llm.NewOpenAICompatibleClient(provider.BaseURL, os.Getenv(provider.APIKeyEnv), name, logger)
		if err != nil {
			return nil, nil, err
		}
		return client, nil, nil
	case "deepseek_web":
		headless := true
		if provider.Headless != nil {
			headless = *provider.Headless
		}
		client, err := deepseekweb.NewClient(deepseekweb.Config{
			ChatURL: provider.ChatURL, LoginURL: provider.LoginURL,
			ProfileDir: provider.ProfileDir, Headless: headless,
		}, logger.With("provider", name))
		if err != nil {
			return nil, nil, err
		}
		return client, client.Close, nil
	default:
		return nil, nil, fmt.Errorf("неподдерживаемый тип провайдера %q", provider.Type)
	}
}

func sortedKeys(set map[string]struct{}) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// writeRoutingReport печатает разрешённую маршрутизацию перед дорогим прогоном: режим,
// доступность провайдеров и провайдера каждой стадии.
func writeRoutingReport(destination io.Writer, routing resolvedRouting) error {
	var report strings.Builder
	fmt.Fprintf(&report, "Режим: %s — %s\n\nПровайдеры:\n", routing.Scheme, routing.Reason)
	for _, provider := range routing.Providers {
		state := "доступен"
		if !provider.Available {
			state = "недоступен"
		}
		fmt.Fprintf(&report, "  %-14s %-12s %s\n", provider.Name, state, provider.Detail)
	}

	report.WriteString("\nСтадии:\n")
	for _, name := range pipelineStageOrder {
		stage, found := routing.Config.Stages[name]
		if !found || len(stage.Targets) == 0 {
			fmt.Fprintf(&report, "  %-10s не настроена\n", name)
			continue
		}
		targets := make([]string, 0, len(stage.Targets))
		for _, target := range stage.Targets {
			targets = append(targets, target.Provider+" / "+target.Model)
		}
		fmt.Fprintf(&report, "  %-10s %-34s %s%s\n", name, strings.Join(targets, " → "), stage.Timeout, stageNote(routing, name))
	}
	// keywords печатается отдельно от стадий статьи: она выполняется в prepare и только
	// тогда, когда Keys.so не нашёл у конкурента ни одного запроса. В отчёте она нужна
	// потому, что это тоже обращение к модели, а отчёт показывает, куда оно пойдёт.
	if stage, found := routing.Config.Stages[keywords.StageName]; found && len(stage.Targets) > 0 {
		targets := make([]string, 0, len(stage.Targets))
		for _, target := range stage.Targets {
			targets = append(targets, target.Provider+" / "+target.Model)
		}
		fmt.Fprintf(&report, "  %-10s %-34s %s  (резерв prepare: Keys.so без запросов)\n",
			keywords.StageName, strings.Join(targets, " → "), stage.Timeout)
	}
	report.WriteString("\n")
	_, err := io.WriteString(destination, report.String())
	return err
}

// pipelineStageOrder — порядок стадий одной статьи. Отдельный список нужен только отчёту:
// map стадий в конфигурации неупорядочена.
var pipelineStageOrder = []string{"structure", "article", "info", "review", "fix", "html"}

func stageNote(routing resolvedRouting, stage string) string {
	switch {
	case stage == "fix":
		return "  (продолжает чат стадии review)"
	case routing.Scheme == schemeDeepSeek && routing.Config.Providers[firstProvider(routing, stage)].SingleChatPerArticle:
		return "  (один чат на статью)"
	default:
		return ""
	}
}

func firstProvider(routing resolvedRouting, stage string) string {
	targets := routing.Config.Stages[stage].Targets
	if len(targets) == 0 {
		return ""
	}
	return targets[0].Provider
}

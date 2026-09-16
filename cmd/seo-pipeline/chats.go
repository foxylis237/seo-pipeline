package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// newSingleSchemeChats поднимает диалоги с моделью по схеме стадий задачи.
//
// Схема одна (наложения у задач, работающих по опубликованным страницам, нет), поэтому
// резолвер режимов здесь не нужен: выбирать между Gemini и DeepSeek не из чего, а конвейер
// generation.Pipeline такая задача не использует вовсе — ей нужен только чат.
//
// Общий у правки и аудита: подъём клиентов, сбор их закрывателей и оборачивание роутера в
// фабрику чатов у них совпадают дословно, а второй копии этого кода в composition root быть
// не должно — разойдётся она молча, и одна из задач останется с незакрытым Chromium.
func newSingleSchemeChats(ctx context.Context, profile tasks.Profile, debugDirs diagnosticsDirs,
	logger *slog.Logger) (taskflow.ChatFactory, func() error, error) {
	noop := func() error { return nil }
	configs, err := loadStageConfigs(profile, logger, true)
	if err != nil {
		return nil, noop, err
	}
	scheme := configs.deepseek
	providers := configs.providers()
	models := configs.models()
	used := make(map[string]struct{})
	for _, stage := range scheme.Stages {
		for _, target := range stage.Targets {
			used[target.Provider] = struct{}{}
		}
	}
	clients := make(map[string]llm.Client, len(used))
	var closers []func() error
	closeAll := func() error {
		var errs []error
		for _, closeClient := range closers {
			if closeErr := closeClient(); closeErr != nil {
				errs = append(errs, fmt.Errorf("закрыть LLM client: %w", closeErr))
			}
		}
		return errors.Join(errs...)
	}
	for _, name := range sortedKeys(used) {
		client, closer, clientErr := newLLMClient(ctx, name, providers[name], models[name], debugDirs.deepseek, logger)
		if clientErr != nil {
			return nil, closeAll, fmt.Errorf("создать LLM provider %q: %w", name, clientErr)
		}
		clients[name] = client
		if closer != nil {
			closers = append(closers, closer)
		}
	}
	return taskflow.NewRouterChats(llm.NewRouter(scheme, clients, logger)), closeAll, nil
}

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/llm/deepseekweb"
)

func runDeepSeekLogin(ctx context.Context, logger *slog.Logger) error {
	provider, err := config.LoadLLMProviderConfig("config/config.yaml", "deepseek_web")
	if err != nil {
		return err
	}
	if provider.Type != "deepseek_web" {
		return fmt.Errorf("DeepSeek Web provider is not configured as llm.providers.deepseek_web")
	}
	return deepseekweb.Login(ctx, deepseekweb.Config{
		ChatURL: provider.ChatURL, LoginURL: provider.LoginURL, ProfileDir: provider.ProfileDir,
	}, logger.With("task", "task_1", "operation", "deepseek-login", "provider", "deepseek_web"))
}

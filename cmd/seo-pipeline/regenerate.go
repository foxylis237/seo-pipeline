package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
)

// regenerateRepository сбрасывает состояние статьи и проверяет итог. Отдельный узкий
// интерфейс: пересоздание не должно уметь ничего сверх этого.
type regenerateRepository interface {
	GetArticleByExternalID(ctx context.Context, externalID string) (article.Article, error)
	ResetGenerationState(ctx context.Context, articleID int64) error
	GetResultInput(ctx context.Context, externalID string) (article.ResultInput, error)
}

// regenerateArtifactWriter удаляет сгенерированные файлы статьи и проверяет наличие итога.
type regenerateArtifactWriter interface {
	ResetGeneratedArtifacts(externalID, slug string) ([]string, error)
	Exists(relativePath string) bool
}

// runRegenerate пересоздаёт одну статью целиком: сбрасывает результаты генерации и снова
// проводит статью полным пайплайном.
//
// Импортированные данные и research Keys.so/Arsenkin сохраняются — пересоздаётся только то,
// что произвела генерация. Поэтому prepare на возобновлении будет пропущен как готовый.
func runRegenerate(
	ctx context.Context,
	repository regenerateRepository,
	artifacts regenerateArtifactWriter,
	runPipeline func(context.Context, string) error,
	logger *slog.Logger,
	externalID string,
) error {
	selected, err := repository.GetArticleByExternalID(ctx, externalID)
	if err != nil {
		return err
	}
	logger.Info("article", "external_id", externalID, "action", "regenerate_started",
		"title", selected.Title, "old_status", selected.Status,
		"old_current_step", optionalText(selected.CurrentStep))

	if err := repository.ResetGenerationState(ctx, selected.ID); err != nil {
		return err
	}
	removed, err := artifacts.ResetGeneratedArtifacts(externalID, "")
	if err != nil {
		return fmt.Errorf("удалить сгенерированные файлы статьи external_id=%s: %w", externalID, err)
	}
	logger.Info("article", "external_id", externalID, "action", "state_reset",
		"removed_count", len(removed), "removed", removed)

	if err := runPipeline(ctx, externalID); err != nil {
		return err
	}
	return verifyRegenerated(ctx, repository, artifacts, logger, externalID)
}

// verifyRegenerated проверяет, что пересозданная статья действительно доведена до конца.
func verifyRegenerated(
	ctx context.Context,
	repository regenerateRepository,
	artifacts regenerateArtifactWriter,
	logger *slog.Logger,
	externalID string,
) error {
	selected, err := repository.GetArticleByExternalID(ctx, externalID)
	if err != nil {
		return err
	}
	result, err := repository.GetResultInput(ctx, externalID)
	if err != nil {
		return err
	}
	var problems []string
	if selected.Status != "completed" {
		problems = append(problems, fmt.Sprintf("статус %s вместо completed", selected.Status))
	}
	if strings.TrimSpace(result.HTMLPath) == "" {
		problems = append(problems, "html_path пуст")
	}
	resultPath := ""
	if paths := strings.TrimSpace(result.ArticlePath); paths != "" {
		resultPath = strings.SplitN(paths, "/", 2)[0] + "/result.md"
	}
	if resultPath == "" || !artifacts.Exists(resultPath) {
		problems = append(problems, "result.md не найден")
	}
	if len(problems) > 0 {
		return fmt.Errorf("пересоздание статьи external_id=%s не завершено: %s",
			externalID, strings.Join(problems, "; "))
	}
	logger.Info("article", "external_id", externalID, "action", "completed",
		"status", selected.Status, "html_path", result.HTMLPath, "result_path", resultPath)
	return nil
}

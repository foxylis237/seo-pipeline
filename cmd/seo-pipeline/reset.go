package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/repository"
)

func runReset(ctx context.Context, articleRepository *repository.ArticleRepository, logger *slog.Logger) error {
	logger.Info("очистка базы данных начата", "operation", "reset")
	if err := articleRepository.Reset(ctx); err != nil {
		return err
	}
	logger.Info("очистка базы данных завершена", "operation", "reset")

	fmt.Println("База данных успешно очищена.")
	fmt.Println("Все таблицы очищены.")
	fmt.Println("Счетчики ID сброшены.")
	fmt.Println()
	fmt.Println("После этого можно сразу выполнить:")
	fmt.Println("go run ./cmd/seo-pipeline task-1 import")
	fmt.Println("go run ./cmd/seo-pipeline task-1 run <external_id>")
	return nil
}

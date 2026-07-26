package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPostgres создаёт пул подключений к PostgreSQL
// и сразу проверяет, что база данных доступна.
func NewPostgres(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {

	// Создаём пул подключений.
	// Само подключение происходит лениво, поэтому далее мы отдельно
	// вызовем Ping(), чтобы убедиться, что база действительно доступна.
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	// Проверяем, что база данных отвечает.
	// Если база недоступна, освобождаем ресурсы пула и возвращаем ошибку.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	// Возвращаем готовый к работе пул подключений.
	return pool, nil
}

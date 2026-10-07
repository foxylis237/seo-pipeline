package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPostgres создаёт пул подключений к PostgreSQL и сразу проверяет, что база доступна.
func NewPostgres(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {

	// Подключение ленивое, доступность проверяет Ping ниже.
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/duplicates"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// catalogRefreshOperation collects the catalog only when it is older than catalogMaxAge.
const catalogRefreshOperation = "catalog-refresh"

// catalogMaxAge keeps obuchim XML-RPC reads to one sync a day: bursts get our IP banned.
const catalogMaxAge = 24 * time.Hour

// syncedCatalogStore is a catalog store that knows when it was last collected.
type syncedCatalogStore interface {
	catalog.Store
	SyncedAt(ctx context.Context) (time.Time, error)
}

// refreshCatalog collects the catalog unless it was collected within catalogMaxAge.
func refreshCatalog(
	ctx context.Context, site catalog.Site, source catalog.Source, store syncedCatalogStore,
	now time.Time, logger *slog.Logger, out io.Writer,
) error {
	syncedAt, err := store.SyncedAt(ctx)
	if err != nil {
		return err
	}
	if !syncedAt.IsZero() && now.Sub(syncedAt) < catalogMaxAge {
		fmt.Fprintf(out, "Каталог услуг свежий (собран %s), обновление пропущено\n",
			syncedAt.Local().Format("02.01.2006 15:04"))
		return nil
	}
	return runCatalogSync(ctx, site, source, store, logger, out)
}

// duplicateGateRepository is what the gate needs to record a stop on the article.
type duplicateGateRepository interface {
	GetArticleByExternalID(ctx context.Context, externalID string) (article.Article, error)
	SaveError(ctx context.Context, articleID int64, processingErr error) error
}

// gateDuplicates stops an article that the duplicate table marks as a duplicate or does not list.
func gateDuplicates(
	table duplicates.Table, repository duplicateGateRepository,
	run func(context.Context, string) error,
) func(context.Context, string) error {
	return func(ctx context.Context, externalID string) error {
		stop := table.Check(externalID)
		if stop == nil {
			return run(ctx, externalID)
		}
		selected, err := repository.GetArticleByExternalID(ctx, externalID)
		if err != nil {
			return fmt.Errorf("%w; статью не найти: %w", stop, err)
		}
		return savePipelineError(ctx, repository, selected.ID, stop)
	}
}

// refreshRunCatalog refreshes the task's catalog at the start of run.
func refreshRunCatalog(
	ctx context.Context, profile tasks.Profile, pool *pgxpool.Pool, settings config.WordPressConfig,
	logger *slog.Logger,
) error {
	site, _, err := catalogSiteFor(profile)
	if err != nil {
		return err
	}
	store, err := catalogStoreFor(profile, pool)
	if err != nil {
		return err
	}
	client, err := newWordPressClient(settings)
	if err != nil {
		return err
	}
	return refreshCatalog(ctx, site, catalogSource{client: client}, store, time.Now(), logger, os.Stdout)
}

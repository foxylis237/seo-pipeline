package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/duplicates"
)

type syncedStubStore struct {
	stubStore
	syncedAt time.Time
	replaced bool
}

func (s *syncedStubStore) Replace(ctx context.Context, programs []catalog.Program) error {
	s.replaced = true
	return s.stubStore.Replace(ctx, programs)
}

func (s *syncedStubStore) SyncedAt(context.Context) (time.Time, error) { return s.syncedAt, nil }

func TestRefreshCatalogOncePerDay(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		syncedAt time.Time
		want     bool
	}{
		"never collected": {time.Time{}, true},
		"older than day":  {now.Add(-25 * time.Hour), true},
		"fresh":           {now.Add(-2 * time.Hour), false},
	} {
		store := &syncedStubStore{syncedAt: tc.syncedAt}
		var out bytes.Buffer
		err := refreshCatalog(context.Background(), catalog.DPOProf(), stubSource{}, store, now,
			slog.New(slog.DiscardHandler), &out)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if store.replaced != tc.want {
			t.Errorf("%s: synced=%v, want %v", name, store.replaced, tc.want)
		}
	}
}

type gateRepository struct {
	saved error
}

func (r *gateRepository) GetArticleByExternalID(_ context.Context, externalID string) (article.Article, error) {
	return article.Article{ID: 1, ExternalID: externalID}, nil
}

func (r *gateRepository) SaveError(_ context.Context, _ int64, err error) error {
	r.saved = err
	return nil
}

func TestGateDuplicatesStopsBeforeGeneration(t *testing.T) {
	table := duplicates.Table{
		"1": {Duplicate: true, Links: []string{"https://site.ru/svarshhik/"}},
		"2": {},
	}
	for id, wantRun := range map[string]bool{"1": false, "2": true, "3": false} {
		repo := &gateRepository{}
		ran := false
		err := gateDuplicates(table, repo, func(context.Context, string) error {
			ran = true
			return nil
		})(context.Background(), id)
		if ran != wantRun {
			t.Errorf("id %s: ran=%v, want %v", id, ran, wantRun)
		}
		if !wantRun && (err == nil || !errors.Is(err, repo.saved)) {
			t.Errorf("id %s: stop not saved on the article: %v", id, err)
		}
	}
	repo := &gateRepository{}
	err := gateDuplicates(table, repo, nil)(context.Background(), "1")
	if err == nil || !strings.Contains(err.Error(), "https://site.ru/svarshhik/") {
		t.Fatalf("duplicate stop lacks the link: %v", err)
	}
}

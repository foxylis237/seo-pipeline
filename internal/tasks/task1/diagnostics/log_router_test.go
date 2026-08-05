package diagnostics

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fileOpener writes stage logs into a temporary directory laid out like the article output.
type fileOpener struct {
	root       string
	knownSlugs map[string]string
}

func (o *fileOpener) OpenArticleLog(externalID, slug, name string) (*os.File, string, error) {
	if slug == "" {
		known, found := o.knownSlugs[externalID]
		if !found {
			return nil, "", os.ErrNotExist
		}
		slug = known
	}
	o.knownSlugs[externalID] = slug
	directory := filepath.Join(o.root, externalID+"-"+slug, "logs")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, "", err
	}
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, "", err
	}
	return file, filepath.ToSlash(filepath.Join(externalID+"-"+slug, "logs", name)), nil
}

func newTestRouter(t *testing.T) (*ArticleLogRouter, *bytes.Buffer, string) {
	t.Helper()
	root := t.TempDir()
	opener := &fileOpener{root: root, knownSlugs: map[string]string{}}
	router := NewArticleLogRouter(opener, "prepare", func(destination io.Writer) slog.Handler {
		return slog.NewTextHandler(destination, nil)
	})
	t.Cleanup(func() { _ = router.Close() })
	var stdout bytes.Buffer
	return router, &stdout, root
}

func readStageLog(t *testing.T, root, directory string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, directory, "logs", "prepare.log"))
	if err != nil {
		t.Fatalf("лог статьи не создан: %v", err)
	}
	return string(data)
}

func TestRouterWritesArticleRecordsIntoItsOwnLog(t *testing.T) {
	router, stdout, root := newTestRouter(t)
	router.Register(7, "52", "kak-stat-logopedom")
	logger := slog.New(router.Handler(slog.NewTextHandler(stdout, nil)))

	logger.With("article_id", int64(7), "external_id", "52").Info("этап начат", "stage", "keysso")
	logger.Info("без статьи", "stage", "start")
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}

	stage := readStageLog(t, root, "52-kak-stat-logopedom")
	if !strings.Contains(stage, "этап начат") || !strings.Contains(stage, "external_id=52") {
		t.Fatalf("в логе статьи нет записи этапа: %q", stage)
	}
	if strings.Contains(stage, "без статьи") {
		t.Fatalf("запись без статьи попала в лог статьи: %q", stage)
	}
	if !strings.Contains(stdout.String(), "без статьи") || !strings.Contains(stdout.String(), "этап начат") {
		t.Fatalf("stdout потерял записи: %q", stdout.String())
	}
}

func TestRouterKeepsArticlesApart(t *testing.T) {
	router, stdout, root := newTestRouter(t)
	router.Register(7, "52", "kak-stat-logopedom")
	router.Register(8, "53", "kak-stat-barista")
	logger := slog.New(router.Handler(slog.NewTextHandler(stdout, nil)))

	logger.With("article_id", int64(7), "external_id", "52").Info("логопед")
	logger.With("article_id", int64(8), "external_id", "53").Info("бариста")
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}

	first := readStageLog(t, root, "52-kak-stat-logopedom")
	second := readStageLog(t, root, "53-kak-stat-barista")
	if !strings.Contains(first, "логопед") || strings.Contains(first, "бариста") {
		t.Fatalf("лог первой статьи = %q", first)
	}
	if !strings.Contains(second, "бариста") || strings.Contains(second, "логопед") {
		t.Fatalf("лог второй статьи = %q", second)
	}
}

func TestRouterRoutesRecordsThatCarryOnlyArticleID(t *testing.T) {
	router, stdout, root := newTestRouter(t)
	router.Register(7, "52", "kak-stat-logopedom")
	logger := slog.New(router.Handler(slog.NewTextHandler(stdout, nil)))

	// Пайплайн логирует обе связки, и роутер запоминает пару.
	logger.With("article_id", int64(7), "external_id", "52").Info("этап статьи")
	// LLM-роутер знает только article_id.
	logger.Info("LLM request attempt started", "article_id", int64(7), "stage", "article")
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}

	stage := readStageLog(t, root, "52-kak-stat-logopedom")
	if !strings.Contains(stage, "LLM request attempt started") {
		t.Fatalf("запись только с article_id не попала в лог статьи: %q", stage)
	}
}

func TestRouterKeepsWorkingWhenLogCannotBeOpened(t *testing.T) {
	router, stdout, _ := newTestRouter(t)
	logger := slog.New(router.Handler(slog.NewTextHandler(stdout, nil)))

	// Статья не зарегистрирована и каталога нет: запись остаётся только в stdout.
	logger.With("article_id", int64(9), "external_id", "99").Info("нет каталога статьи")
	logger.With("article_id", int64(9), "external_id", "99").Info("и ещё одна запись")
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "нет каталога статьи") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRouterKeepsAttributesAddedWithWith(t *testing.T) {
	router, stdout, root := newTestRouter(t)
	router.Register(7, "52", "kak-stat-logopedom")
	logger := slog.New(router.Handler(slog.NewTextHandler(stdout, nil))).
		With("task", "task_1", "operation", "prepare").
		With("article_id", int64(7), "external_id", "52")

	logger.Info("этап начат", "stage", "keysso")
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}

	stage := readStageLog(t, root, "52-kak-stat-logopedom")
	for _, want := range []string{"task=task_1", "operation=prepare", "article_id=7", "stage=keysso"} {
		if !strings.Contains(stage, want) {
			t.Errorf("в логе статьи нет %q: %s", want, stage)
		}
	}
}

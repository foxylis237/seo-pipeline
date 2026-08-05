package diagnostics

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// LogFileOpener opens the stage log of one article. It is implemented by the article output
// writer, which owns the <external_id>-<slug>/logs/ layout. An empty slug means the writer
// resolves the existing article directory itself.
type LogFileOpener interface {
	OpenArticleLog(externalID, slug, name string) (*os.File, string, error)
}

// ArticleLogRouter duplicates log records into the stage log of the article they belong to,
// so that a run no longer has to be piped through tee to be kept.
//
// Routing is by the article_id and external_id attributes that stage loggers already carry:
// no call site has to know about the file. Стадии после prepare маршрутизируются сами —
// каталог статьи уже существует; Register нужен только там, где каталог ещё не создан.
type ArticleLogRouter struct {
	opener LogFileOpener
	// stageLog is the file name of the running operation, e.g. "prepare.log".
	stageLog string
	newFile  func(io.Writer) slog.Handler

	mu         sync.Mutex
	slugs      map[string]string // external_id -> slug
	byArticle  map[string]string // article_id -> external_id
	files      map[string]*os.File
	handlers   map[string]slog.Handler
	logPaths   map[string]string
	openFailed map[string]struct{}
}

// NewArticleLogRouter creates a router writing <stage>.log next to the article artifacts.
// newFile builds the file handler, so stage logs keep the format configured for the run.
func NewArticleLogRouter(opener LogFileOpener, stage string, newFile func(io.Writer) slog.Handler) *ArticleLogRouter {
	return &ArticleLogRouter{
		opener: opener, stageLog: stage + ".log", newFile: newFile,
		slugs: map[string]string{}, byArticle: map[string]string{},
		files: map[string]*os.File{}, handlers: map[string]slog.Handler{},
		logPaths: map[string]string{}, openFailed: map[string]struct{}{},
	}
}

// Register supplies the slug of an article whose directory may not exist yet, so that the
// very first prepare of an article still gets its own log.
func (r *ArticleLogRouter) Register(articleID int64, externalID, slug string) {
	if r == nil || strings.TrimSpace(externalID) == "" || strings.TrimSpace(slug) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.slugs[externalID] = slug
	if articleID > 0 {
		r.byArticle[strconv.FormatInt(articleID, 10)] = externalID
	}
}

// LogPath returns the stage log path of one article, relative to the output root, once it
// has been opened.
func (r *ArticleLogRouter) LogPath(externalID string) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logPaths[externalID]
}

// Handler wraps the base handler so that every record it receives is also routed.
func (r *ArticleLogRouter) Handler(base slog.Handler) slog.Handler {
	if r == nil {
		return base
	}
	return &routingHandler{base: base, router: r}
}

// Close closes every opened stage log.
func (r *ArticleLogRouter) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var closeErr error
	for externalID, file := range r.files {
		if err := file.Close(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("закрыть лог статьи %s: %w", externalID, err)
		}
		delete(r.files, externalID)
		delete(r.handlers, externalID)
	}
	return closeErr
}

// handlerFor returns the file handler of one article, opening the file on first use.
func (r *ArticleLogRouter) handlerFor(externalID string) slog.Handler {
	r.mu.Lock()
	defer r.mu.Unlock()
	if handler, found := r.handlers[externalID]; found {
		return handler
	}
	if _, failed := r.openFailed[externalID]; failed {
		return nil
	}
	slug := r.slugs[externalID]
	file, relativePath, err := r.opener.OpenArticleLog(externalID, slug, r.stageLog)
	if err != nil {
		// Логи статьи — вспомогательные: обработку статьи их отсутствие не останавливает.
		r.openFailed[externalID] = struct{}{}
		return nil
	}
	handler := r.newFile(file)
	r.files[externalID] = file
	r.handlers[externalID] = handler
	r.logPaths[externalID] = relativePath
	return handler
}

// externalIDFor resolves the article an id-only record belongs to.
func (r *ArticleLogRouter) externalIDFor(articleID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	externalID, found := r.byArticle[articleID]
	return externalID, found
}

// learn remembers the article_id ↔ external_id pair seen in a record, so that records
// carrying only article_id — LLM router requests, for one — route as well.
func (r *ArticleLogRouter) learn(articleID, externalID string) {
	if articleID == "" || externalID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byArticle[articleID] = externalID
}

type routingHandler struct {
	base   slog.Handler
	router *ArticleLogRouter
	// identity carries article_id and external_id already fixed by With calls.
	articleID  string
	externalID string
	attrs      []slog.Attr
	groups     []string
	// file is the per-destination handler with the same attrs and groups applied.
	file    slog.Handler
	fileFor string
}

func (h *routingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *routingHandler) Handle(ctx context.Context, record slog.Record) error {
	baseErr := h.base.Handle(ctx, record)
	articleID, externalID := h.articleID, h.externalID
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "article_id":
			if value := attributeText(attr.Value); value != "" && value != "0" {
				articleID = value
			}
		case "external_id":
			if value := attributeText(attr.Value); value != "" {
				externalID = value
			}
		}
		return true
	})
	h.router.learn(articleID, externalID)
	if externalID == "" && articleID != "" {
		if resolved, found := h.router.externalIDFor(articleID); found {
			externalID = resolved
		}
	}
	if externalID == "" {
		return baseErr
	}
	file := h.fileHandler(externalID)
	if file == nil || !file.Enabled(ctx, record.Level) {
		return baseErr
	}
	if err := file.Handle(ctx, record.Clone()); err != nil && baseErr == nil {
		return err
	}
	return baseErr
}

// fileHandler applies the attrs and groups of this handler to the article log handler once.
func (h *routingHandler) fileHandler(externalID string) slog.Handler {
	if h.file != nil && h.fileFor == externalID {
		return h.file
	}
	handler := h.router.handlerFor(externalID)
	if handler == nil {
		return nil
	}
	for _, group := range h.groups {
		handler = handler.WithGroup(group)
	}
	if len(h.attrs) > 0 {
		handler = handler.WithAttrs(h.attrs)
	}
	h.file, h.fileFor = handler, externalID
	return handler
}

func (h *routingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := h.clone()
	clone.base = h.base.WithAttrs(attrs)
	clone.attrs = append(clone.attrs, attrs...)
	for _, attr := range attrs {
		switch attr.Key {
		case "article_id":
			if value := attributeText(attr.Value); value != "" && value != "0" {
				clone.articleID = value
			}
		case "external_id":
			if value := attributeText(attr.Value); value != "" {
				clone.externalID = value
			}
		}
	}
	return clone
}

func (h *routingHandler) WithGroup(name string) slog.Handler {
	clone := h.clone()
	clone.base = h.base.WithGroup(name)
	clone.groups = append(clone.groups, name)
	return clone
}

func (h *routingHandler) clone() *routingHandler {
	return &routingHandler{
		base: h.base, router: h.router, articleID: h.articleID, externalID: h.externalID,
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: append([]string(nil), h.groups...),
	}
}

func attributeText(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		return value.String()
	case slog.KindInt64:
		return strconv.FormatInt(value.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(value.Uint64(), 10)
	default:
		return strings.TrimSpace(value.String())
	}
}

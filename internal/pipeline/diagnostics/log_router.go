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

// LogFileOpener opens the stage log of one article; an empty slug means the existing directory.
type LogFileOpener interface {
	OpenArticleLog(externalID, slug, name string) (*os.File, string, error)
}

// ArticleLogRouter duplicates log records into the stage log of the article they belong to,
// routing by the article_id and external_id attributes.
type ArticleLogRouter struct {
	opener   LogFileOpener
	stageLog string
	version  string
	newFile  func(io.Writer) slog.Handler

	mu         sync.Mutex
	slugs      map[string]string // external_id -> slug
	byArticle  map[string]string // article_id -> external_id
	files      map[string]*os.File
	handlers   map[string]slog.Handler
	logPaths   map[string]string
	openFailed map[string]struct{}
}

// NewArticleLogRouter creates a router writing <stage>.log next to the article artifacts;
// every opened log starts with the code version.
func NewArticleLogRouter(opener LogFileOpener, stage, version string, newFile func(io.Writer) slog.Handler) *ArticleLogRouter {
	return &ArticleLogRouter{
		opener: opener, stageLog: stage + ".log", version: version, newFile: newFile,
		slugs: map[string]string{}, byArticle: map[string]string{},
		files: map[string]*os.File{}, handlers: map[string]slog.Handler{},
		logPaths: map[string]string{}, openFailed: map[string]struct{}{},
	}
}

// Register supplies the slug of an article whose directory may not exist yet.
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

// LogPath returns the opened stage log path of one article, relative to the output root.
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
		r.openFailed[externalID] = struct{}{}
		return nil
	}
	handler := r.newFile(file)
	slog.New(handler).Info("article log opened", "external_id", externalID, "version", r.version)
	r.files[externalID] = file
	r.handlers[externalID] = handler
	r.logPaths[externalID] = relativePath
	return handler
}

func (r *ArticleLogRouter) externalIDFor(articleID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	externalID, found := r.byArticle[articleID]
	return externalID, found
}

// learn lets records carrying only article_id route as well.
func (r *ArticleLogRouter) learn(articleID, externalID string) {
	if articleID == "" || externalID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byArticle[articleID] = externalID
}

type routingHandler struct {
	base       slog.Handler
	router     *ArticleLogRouter
	articleID  string
	externalID string
	attrs      []slog.Attr
	groups     []string
	// mu: the internal/llm heartbeat goroutine logs through the same logger concurrently.
	mu      sync.Mutex
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

func (h *routingHandler) fileHandler(externalID string) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
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

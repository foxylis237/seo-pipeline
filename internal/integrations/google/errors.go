package google

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Сигнальные ошибки, повтор которых бессмыслен: браузер в этих состояниях ждёт человека.
// Классификация идёт по типам и значениям, а не по подстрокам сообщения, — как у Keys.so
// и Arsenkin.
var (
	// ErrSessionExpired — сохранённый профиль есть, но Google больше не считает его
	// авторизованным.
	ErrSessionExpired = errors.New("сессия Google истекла: выполните make login google")
	// ErrLoginRequired — профиля нет вовсе, входа никогда не было.
	ErrLoginRequired = errors.New("нет профиля Google: выполните make login google")
	// ErrManualVerification — Google показал CAPTCHA или запросил 2FA. Обходить их нельзя,
	// и повторять тоже: следующая попытка упрётся в ту же проверку.
	ErrManualVerification = errors.New("требуется ручная проверка Google (CAPTCHA или 2FA): выполните make login google")
	// ErrProfileBusy — профилем уже пользуется другой процесс.
	ErrProfileBusy = errors.New("профиль Google занят другим процессом")
)

// StageError передаёт причину отказа вместе с тем, где он случился и лечится ли повтором.
type StageError struct {
	ArticleID  int64
	ExternalID string
	// Stage — что именно делали: find_document, create_document, replace_document и так далее.
	Stage string
	// Retryable отвечает на единственный вопрос, который задаёт повтор.
	Retryable bool
	Err       error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("публикация в Google Docs stage=%s: %v", e.Stage, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

// IsRetryable отвечает, имеет ли смысл повторять. Сигнальные ошибки ручного вмешательства
// перевешивают флаг: они не становятся временными оттого, что их так пометили выше по стеку.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	for _, terminal := range []error{ErrSessionExpired, ErrLoginRequired, ErrManualVerification, ErrProfileBusy} {
		if errors.Is(err, terminal) {
			return false
		}
	}
	var stageErr *StageError
	if errors.As(err, &stageErr) {
		return stageErr.Retryable
	}
	return false
}

// NeedsManualLogin отличает отказы, которые чинит только человек за браузером. Вызывающему
// это нужно, чтобы подсказать google-login вместо бесполезного «попробуйте позже».
func NeedsManualLogin(err error) bool {
	return errors.Is(err, ErrSessionExpired) ||
		errors.Is(err, ErrLoginRequired) ||
		errors.Is(err, ErrManualVerification)
}

// RetryPolicy ограничивает повторы временных отказов Google и Playwright.
type RetryPolicy struct {
	MaxAttempts int
	// BaseDelay — задержка перед второй попыткой; дальше удваивается.
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

// DefaultRetryPolicy — три попытки с небольшим ростом паузы. Больше нет смысла: если Google
// не ответил за три захода, это уже не рябь, а состояние, требующее внимания.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: 2 * time.Second, MaxDelay: 15 * time.Second}
}

// Backoff возвращает паузу перед попыткой, следующей за номером attempt.
func (p RetryPolicy) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := p.BaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if p.MaxDelay > 0 && delay >= p.MaxDelay {
			return p.MaxDelay
		}
	}
	if p.MaxDelay > 0 && delay > p.MaxDelay {
		return p.MaxDelay
	}
	return delay
}

// Observer получает исход каждой попытки. Отдельный интерфейс, а не *slog.Logger внутри
// публикатора: так тест проверяет последовательность попыток, не разбирая текст лога.
type Observer interface {
	Succeeded(job Job, result Result, attempt int, elapsed time.Duration)
	Failed(job Job, attempt int, elapsed time.Duration, retryable bool, err error)
}

type noopObserver struct{}

func (noopObserver) Succeeded(Job, Result, int, time.Duration)   {}
func (noopObserver) Failed(Job, int, time.Duration, bool, error) {}

// SlogObserver пишет исход попыток в slog с полями, которых ждёт диагностика прогона.
type SlogObserver struct{ Logger *slog.Logger }

func (o SlogObserver) Succeeded(job Job, result Result, attempt int, elapsed time.Duration) {
	action := "обновлён"
	if result.Created {
		action = "создан"
	}
	o.Logger.Info("промпт опубликован в Google Docs",
		"article_id", job.ArticleID, "external_id", job.ExternalID, "stage", "google_publish",
		"attempt", attempt, "duration_ms", elapsed.Milliseconds(),
		"action", action, "document_url", result.DocumentURL)
}

func (o SlogObserver) Failed(job Job, attempt int, elapsed time.Duration, retryable bool, err error) {
	level := slog.LevelWarn
	if !retryable {
		level = slog.LevelError
	}
	stage := "google_publish"
	var stageErr *StageError
	if errors.As(err, &stageErr) && stageErr.Stage != "" {
		stage = "google_" + stageErr.Stage
	}
	o.Logger.Log(context.Background(), level, "публикация промпта в Google Docs не удалась",
		"article_id", job.ArticleID, "external_id", job.ExternalID, "stage", stage,
		"attempt", attempt, "duration_ms", elapsed.Milliseconds(),
		"retryable", retryable, "needs_manual_login", NeedsManualLogin(err), "error", err)
}

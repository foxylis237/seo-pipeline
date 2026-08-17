// Package google публикует готовый промпт статьи в Google Docs через веб-интерфейс и
// persistent-профиль Playwright.
//
// Пакет ничего не генерирует и не обращается к LLM: он получает уже собранный промпт, который
// пайплайн отправил модели и сохранил в prompts/article_prompt.txt, и кладёт его в документ.
// Это единственный контракт — публикация не имеет права влиять на содержимое промпта.
package google

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TitlePrefix — начало имени документа. Имя целиком служит ключом поиска: документ с таким же
// именем перезаписывается, нового не создаётся.
const TitlePrefix = "Промт: "

// DocumentTitle возвращает имя документа для статьи.
func DocumentTitle(articleTitle string) string {
	return TitlePrefix + strings.TrimSpace(articleTitle)
}

// Job — всё, что нужно для одной публикации. Промпт передаётся значением, а не путём к файлу:
// читает его вызывающий, и пакет не может случайно взять с диска не ту версию.
type Job struct {
	ArticleID  int64
	ExternalID string
	// ArticleTitle — название статьи, из которого собирается имя документа.
	ArticleTitle string
	// Prompt — текст, отправленный модели на стадии article.
	Prompt string
}

// Validate проверяет задание до открытия браузера: запускать Chromium ради заведомо
// непригодных данных бессмысленно.
func (j Job) Validate() error {
	if strings.TrimSpace(j.ExternalID) == "" {
		return fmt.Errorf("external_id пуст")
	}
	if strings.TrimSpace(j.ArticleTitle) == "" {
		return fmt.Errorf("название статьи пусто")
	}
	if strings.TrimSpace(j.Prompt) == "" {
		return fmt.Errorf("промпт пуст")
	}
	return nil
}

// Result описывает, чем закончилась публикация.
type Result struct {
	// Created отличает созданный документ от перезаписанного. Нужен логу и отчёту команды.
	Created bool
	// DocumentURL — адрес документа после публикации.
	DocumentURL string
	Attempts    int
}

// Session — то, что публикация требует от браузера. Интерфейс объявлен здесь, у потребителя,
// а реализуется браузерным слоем: благодаря этому решение «создать или перезаписать», разбор
// ошибок и повторы проверяются тестами без настоящего Google.
type Session interface {
	// FindDocument ищет документ по точному имени в папке. Отсутствие документа — не ошибка.
	FindDocument(ctx context.Context, title string) (documentURL string, found bool, err error)
	// CreateDocument создаёт документ с этим именем и содержимым и возвращает его адрес.
	CreateDocument(ctx context.Context, title, body string) (documentURL string, err error)
	// ReplaceDocument полностью заменяет содержимое существующего документа.
	ReplaceDocument(ctx context.Context, documentURL, body string) error
	Close() error
}

// SessionFactory открывает браузерную сессию. Отдельный тип нужен, чтобы повтор после
// временной ошибки поднимал браузер заново: половина отказов Playwright лечится только
// новым контекстом.
type SessionFactory func(ctx context.Context) (Session, error)

// Publisher публикует промпты. Один экземпляр на процесс: за persistent-профиль держится
// flock, и две одновременные сессии его портят.
type Publisher struct {
	newSession SessionFactory
	retry      RetryPolicy
	now        func() time.Time
	sleep      func(ctx context.Context, d time.Duration) error
}

// NewPublisher собирает публикатор. now и sleep подменяются в тестах, чтобы повторы
// проверялись без реального ожидания.
func NewPublisher(newSession SessionFactory, retry RetryPolicy) *Publisher {
	return &Publisher{
		newSession: newSession,
		retry:      retry,
		now:        time.Now,
		sleep:      sleepContext,
	}
}

// Publish создаёт или перезаписывает документ статьи.
//
// Порядок повторов: каждая попытка открывает свою сессию и закрывает её за собой. Ошибка,
// которую повтор не лечит — истёкшая сессия, требование CAPTCHA или 2FA, — прекращает работу
// сразу: браузер в этих случаях ждёт человека, а не следующей попытки.
func (p *Publisher) Publish(ctx context.Context, job Job, observer Observer) (Result, error) {
	if err := job.Validate(); err != nil {
		return Result{}, &StageError{
			ArticleID: job.ArticleID, ExternalID: job.ExternalID,
			Stage: "validate", Retryable: false, Err: err,
		}
	}
	if observer == nil {
		observer = noopObserver{}
	}

	var lastErr error
	for attempt := 1; attempt <= p.retry.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Result{}, &StageError{
				ArticleID: job.ArticleID, ExternalID: job.ExternalID,
				Stage: "canceled", Retryable: false, Err: err,
			}
		}
		started := p.now()
		result, err := p.publishOnce(ctx, job)
		elapsed := p.now().Sub(started)
		if err == nil {
			result.Attempts = attempt
			observer.Succeeded(job, result, attempt, elapsed)
			return result, nil
		}
		lastErr = err
		retryable := IsRetryable(err)
		observer.Failed(job, attempt, elapsed, retryable, err)
		if !retryable || attempt == p.retry.MaxAttempts {
			break
		}
		if sleepErr := p.sleep(ctx, p.retry.Backoff(attempt)); sleepErr != nil {
			return Result{}, &StageError{
				ArticleID: job.ArticleID, ExternalID: job.ExternalID,
				Stage: "canceled", Retryable: false, Err: sleepErr,
			}
		}
	}
	return Result{}, lastErr
}

// publishOnce — одна попытка целиком, вместе с открытием и закрытием сессии.
func (p *Publisher) publishOnce(ctx context.Context, job Job) (result Result, returnErr error) {
	session, err := p.newSession(ctx)
	if err != nil {
		return Result{}, wrapStage(job, "open_session", err)
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil && returnErr == nil {
			returnErr = wrapStage(job, "close_session", closeErr)
		}
	}()

	title := DocumentTitle(job.ArticleTitle)
	documentURL, found, err := session.FindDocument(ctx, title)
	if err != nil {
		return Result{}, wrapStage(job, "find_document", err)
	}
	// Создание и перезапись — единственная развилка команды. Копий с суффиксом (1) не
	// появляется именно потому, что поиск идёт раньше создания.
	if found {
		if err := session.ReplaceDocument(ctx, documentURL, job.Prompt); err != nil {
			return Result{}, wrapStage(job, "replace_document", err)
		}
		return Result{Created: false, DocumentURL: documentURL}, nil
	}
	created, err := session.CreateDocument(ctx, title, job.Prompt)
	if err != nil {
		return Result{}, wrapStage(job, "create_document", err)
	}
	return Result{Created: true, DocumentURL: created}, nil
}

// wrapStage сохраняет уже классифицированную ошибку и заворачивает остальные как временные:
// неизвестный отказ браузера чаще лечится повтором, чем нет.
func wrapStage(job Job, stage string, err error) error {
	var stageErr *StageError
	if errors.As(err, &stageErr) {
		if stageErr.ArticleID == 0 {
			stageErr.ArticleID = job.ArticleID
		}
		if stageErr.ExternalID == "" {
			stageErr.ExternalID = job.ExternalID
		}
		return stageErr
	}
	return &StageError{
		ArticleID: job.ArticleID, ExternalID: job.ExternalID,
		Stage: stage, Retryable: true, Err: err,
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

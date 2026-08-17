package google

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSession подменяет браузер: решение «создать или перезаписать», повторы и классификация
// ошибок проверяются целиком без настоящего Google.
type fakeSession struct {
	existingURL string
	found       bool
	findErr     error
	createErr   error
	replaceErr  error

	findCalls    int
	createCalls  int
	replaceCalls int
	closeCalls   int
	createdTitle string
	writtenBody  string
}

func (s *fakeSession) FindDocument(context.Context, string) (string, bool, error) {
	s.findCalls++
	if s.findErr != nil {
		return "", false, s.findErr
	}
	return s.existingURL, s.found, nil
}

func (s *fakeSession) CreateDocument(_ context.Context, title, body string) (string, error) {
	s.createCalls++
	if s.createErr != nil {
		return "", s.createErr
	}
	s.createdTitle = title
	s.writtenBody = body
	return "https://docs.google.com/document/d/new/edit", nil
}

func (s *fakeSession) ReplaceDocument(_ context.Context, _, body string) error {
	s.replaceCalls++
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.writtenBody = body
	return nil
}

func (s *fakeSession) Close() error {
	s.closeCalls++
	return nil
}

// newTestPublisher собирает публикатор без реального ожидания между попытками.
func newTestPublisher(t *testing.T, sessions ...*fakeSession) (*Publisher, *int) {
	t.Helper()
	opened := 0
	index := 0
	publisher := NewPublisher(func(context.Context) (Session, error) {
		opened++
		session := sessions[index]
		if index < len(sessions)-1 {
			index++
		}
		return session, nil
	}, RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond})
	publisher.sleep = func(context.Context, time.Duration) error { return nil }
	return publisher, &opened
}

func testJob() Job {
	return Job{ArticleID: 9, ExternalID: "45", ArticleTitle: "Как выбрать фрезу", Prompt: "готовый промпт"}
}

func TestDocumentTitleUsesPrefix(t *testing.T) {
	if got := DocumentTitle("Как выбрать фрезу"); got != "Промт: Как выбрать фрезу" {
		t.Fatalf("получено %q", got)
	}
	// Лишние пробелы вокруг названия ломали бы поиск по точному имени.
	if got := DocumentTitle("  Как выбрать фрезу  "); got != "Промт: Как выбрать фрезу" {
		t.Fatalf("пробелы не срезаны: %q", got)
	}
}

// Документа нет — создаём.
func TestPublishCreatesWhenDocumentIsMissing(t *testing.T) {
	session := &fakeSession{found: false}
	publisher, _ := newTestPublisher(t, session)

	result, err := publisher.Publish(context.Background(), testJob(), nil)
	if err != nil {
		t.Fatalf("Publish вернул ошибку: %v", err)
	}
	if !result.Created {
		t.Fatal("ожидалось создание документа")
	}
	if session.createCalls != 1 || session.replaceCalls != 0 {
		t.Fatalf("create=%d replace=%d", session.createCalls, session.replaceCalls)
	}
	if session.createdTitle != "Промт: Как выбрать фрезу" {
		t.Fatalf("имя документа %q", session.createdTitle)
	}
	if session.writtenBody != "готовый промпт" {
		t.Fatalf("опубликован не тот текст: %q", session.writtenBody)
	}
}

// Документ есть — перезаписываем его, копии (1) не появляется.
func TestPublishReplacesExistingDocument(t *testing.T) {
	session := &fakeSession{found: true, existingURL: "https://docs.google.com/document/d/old/edit"}
	publisher, _ := newTestPublisher(t, session)

	result, err := publisher.Publish(context.Background(), testJob(), nil)
	if err != nil {
		t.Fatalf("Publish вернул ошибку: %v", err)
	}
	if result.Created {
		t.Fatal("существующий документ не должен создаваться заново")
	}
	if session.createCalls != 0 {
		t.Fatalf("создание вызвано %d раз при существующем документе", session.createCalls)
	}
	if session.replaceCalls != 1 {
		t.Fatalf("перезапись вызвана %d раз", session.replaceCalls)
	}
	if result.DocumentURL != session.existingURL {
		t.Fatalf("вернулся адрес %q вместо адреса найденного документа", result.DocumentURL)
	}
}

// Поиск обязан идти раньше создания: именно этот порядок и не даёт плодить копии.
func TestPublishSearchesBeforeCreating(t *testing.T) {
	session := &fakeSession{found: false}
	publisher, _ := newTestPublisher(t, session)

	if _, err := publisher.Publish(context.Background(), testJob(), nil); err != nil {
		t.Fatalf("Publish вернул ошибку: %v", err)
	}
	if session.findCalls != 1 {
		t.Fatalf("поиск вызван %d раз", session.findCalls)
	}
}

// Временная ошибка повторяется, и вторая попытка доводит дело до конца.
func TestPublishRetriesTemporaryFailure(t *testing.T) {
	failing := &fakeSession{createErr: &StageError{Stage: "create_document", Retryable: true, Err: errors.New("таймаут")}}
	succeeding := &fakeSession{found: false}
	publisher, opened := newTestPublisher(t, failing, succeeding)

	result, err := publisher.Publish(context.Background(), testJob(), nil)
	if err != nil {
		t.Fatalf("Publish вернул ошибку: %v", err)
	}
	if result.Attempts != 2 {
		t.Fatalf("попыток %d, ожидалось 2", result.Attempts)
	}
	if *opened != 2 {
		t.Fatalf("сессий открыто %d: повтор обязан поднимать браузер заново", *opened)
	}
}

// Попытки ограничены: бесконечно долбить Google нельзя.
func TestPublishStopsAfterMaxAttempts(t *testing.T) {
	session := &fakeSession{createErr: &StageError{Stage: "create_document", Retryable: true, Err: errors.New("таймаут")}}
	publisher, opened := newTestPublisher(t, session)

	if _, err := publisher.Publish(context.Background(), testJob(), nil); err == nil {
		t.Fatal("ожидалась ошибка после исчерпания попыток")
	}
	if *opened != 3 {
		t.Fatalf("сессий открыто %d, ожидалось 3", *opened)
	}
}

// Отказы, которые лечит только человек, не повторяются: следующая попытка упрётся в то же.
func TestPublishDoesNotRetryManualFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "истёкшая сессия", err: ErrSessionExpired},
		{name: "нет входа", err: ErrLoginRequired},
		{name: "CAPTCHA или 2FA", err: ErrManualVerification},
		{name: "профиль занят", err: ErrProfileBusy},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			session := &fakeSession{findErr: &StageError{Stage: "find_document", Retryable: false, Err: testCase.err}}
			publisher, opened := newTestPublisher(t, session)

			_, err := publisher.Publish(context.Background(), testJob(), nil)
			if err == nil {
				t.Fatal("ожидалась ошибка")
			}
			if !errors.Is(err, testCase.err) {
				t.Fatalf("причина потеряна: %v", err)
			}
			if *opened != 1 {
				t.Fatalf("сессий открыто %d: повторять эту ошибку нельзя", *opened)
			}
		})
	}
}

// Даже если ошибку выше пометили временной, ручные отказы остаются неповторяемыми.
func TestManualFailureOverridesRetryableFlag(t *testing.T) {
	err := &StageError{Stage: "find_document", Retryable: true, Err: ErrSessionExpired}
	if IsRetryable(err) {
		t.Fatal("истёкшая сессия не становится временной ошибкой от флага")
	}
	if !NeedsManualLogin(err) {
		t.Fatal("отказ должен опознаваться как требующий ручного входа")
	}
}

// Сессия закрывается после каждой попытки, включая неудачную: иначе Chromium переживёт команду.
func TestPublishClosesSessionOnEveryAttempt(t *testing.T) {
	session := &fakeSession{createErr: &StageError{Stage: "create_document", Retryable: true, Err: errors.New("таймаут")}}
	publisher, _ := newTestPublisher(t, session)

	if _, err := publisher.Publish(context.Background(), testJob(), nil); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if session.closeCalls != 3 {
		t.Fatalf("сессия закрыта %d раз при 3 попытках", session.closeCalls)
	}
}

// Отменённый контекст прекращает работу и не считается временной ошибкой.
func TestPublishStopsOnCancelledContext(t *testing.T) {
	session := &fakeSession{found: false}
	publisher, opened := newTestPublisher(t, session)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := publisher.Publish(ctx, testJob(), nil)
	if err == nil {
		t.Fatal("ожидалась ошибка отмены")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("причина отмены потеряна: %v", err)
	}
	if *opened != 0 {
		t.Fatal("при отменённом контексте браузер открываться не должен")
	}
}

// Пустой промпт до браузера не доходит: открывать Chromium ради заведомо негодных данных
// бессмысленно, а пустой документ затёр бы прошлый промпт.
func TestPublishRejectsEmptyJobBeforeOpeningBrowser(t *testing.T) {
	cases := map[string]Job{
		"пустой промпт": {ExternalID: "45", ArticleTitle: "Заголовок", Prompt: "   "},
		"пустое имя":    {ExternalID: "45", ArticleTitle: "  ", Prompt: "промпт"},
		"пустой ID":     {ExternalID: "", ArticleTitle: "Заголовок", Prompt: "промпт"},
	}
	for name, job := range cases {
		t.Run(name, func(t *testing.T) {
			session := &fakeSession{}
			publisher, opened := newTestPublisher(t, session)
			if _, err := publisher.Publish(context.Background(), job, nil); err == nil {
				t.Fatal("ожидалась ошибка проверки задания")
			}
			if *opened != 0 {
				t.Fatal("браузер не должен открываться для негодного задания")
			}
		})
	}
}

// Публикация получает уже готовый промпт и не пересобирает его: единственный источник — Job.
func TestPublishSendsPromptAsIs(t *testing.T) {
	session := &fakeSession{found: true, existingURL: "https://docs.google.com/document/d/old/edit"}
	publisher, _ := newTestPublisher(t, session)
	job := testJob()
	job.Prompt = "Строка 1\n\nСтрока 2 с пробелами в конце   \nи хвост"

	if _, err := publisher.Publish(context.Background(), job, nil); err != nil {
		t.Fatalf("Publish вернул ошибку: %v", err)
	}
	if session.writtenBody != job.Prompt {
		t.Fatalf("промпт изменён при публикации:\nбыло: %q\nстало: %q", job.Prompt, session.writtenBody)
	}
}

// Observer получает каждую попытку: по этим полям и читается диагностика прогона.
func TestObserverSeesEveryAttempt(t *testing.T) {
	session := &fakeSession{createErr: &StageError{Stage: "create_document", Retryable: true, Err: errors.New("таймаут")}}
	publisher, _ := newTestPublisher(t, session)
	observer := &recordingObserver{}

	if _, err := publisher.Publish(context.Background(), testJob(), observer); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if len(observer.failures) != 3 {
		t.Fatalf("получено %d отчётов о неудаче, ожидалось 3", len(observer.failures))
	}
	for index, attempt := range observer.failures {
		if attempt != index+1 {
			t.Fatalf("номера попыток не по порядку: %v", observer.failures)
		}
	}
}

type recordingObserver struct {
	mu        sync.Mutex
	failures  []int
	successes []int
}

func (o *recordingObserver) Succeeded(_ Job, _ Result, attempt int, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.successes = append(o.successes, attempt)
}

func (o *recordingObserver) Failed(_ Job, attempt int, _ time.Duration, _ bool, _ error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failures = append(o.failures, attempt)
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 4 * time.Second}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	for index, expected := range want {
		if got := policy.Backoff(index + 1); got != expected {
			t.Fatalf("попытка %d: пауза %v, ожидалась %v", index+1, got, expected)
		}
	}
}

func TestFolderIDFromURL(t *testing.T) {
	id, err := FolderID(DefaultFolderURL)
	if err != nil {
		t.Fatalf("FolderID вернул ошибку: %v", err)
	}
	if id != "1N-NRlswacwqKWUOEiA1OS3tKT_V_yLiS" {
		t.Fatalf("получен идентификатор %q", id)
	}
	for _, broken := range []string{"", "https://drive.google.com/drive/", "не адрес"} {
		if _, err := FolderID(broken); err == nil {
			t.Fatalf("для %q ожидалась ошибка", broken)
		}
	}
}

func TestClassifyPageDetectsManualStates(t *testing.T) {
	cases := []struct {
		url  string
		want error
	}{
		// Обычная страница входа — это истёкшая сессия, а не проверка: лечится google-login.
		{url: "https://accounts.google.com/signin/v2/identifier", want: ErrSessionExpired},
		{url: "https://accounts.google.com/ServiceLogin", want: ErrSessionExpired},
		// А вот challenge — это уже 2FA или CAPTCHA, и подсказка человеку другая.
		{url: "https://accounts.google.com/signin/challenge/pwd", want: ErrManualVerification},
		{url: "https://www.google.com/sorry/index", want: ErrManualVerification},
		{url: "https://docs.google.com/document/d/abc/edit", want: nil},
		{url: "https://drive.google.com/drive/folders/abc", want: nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.url, func(t *testing.T) {
			got := classifyPage(testCase.url)
			if testCase.want == nil {
				if got != nil {
					t.Fatalf("рабочая страница принята за отказ: %v", got)
				}
				return
			}
			if !errors.Is(got, testCase.want) {
				t.Fatalf("получено %v, ожидалось %v", got, testCase.want)
			}
		})
	}
}

// Неизвестный отказ браузера считается временным: он чаще лечится повтором, чем нет.
func TestUnknownFailureIsRetryable(t *testing.T) {
	err := wrapStage(testJob(), "find_document", fmt.Errorf("playwright: %w", errors.New("connection closed")))
	if !IsRetryable(err) {
		t.Fatal("неизвестная ошибка браузера должна быть временной")
	}
	if !strings.Contains(err.Error(), "find_document") {
		t.Fatalf("этап потерян в сообщении: %v", err)
	}
}

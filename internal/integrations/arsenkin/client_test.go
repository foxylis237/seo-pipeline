package arsenkin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeResults(t *testing.T) {
	rows := []rawKeywordFrequency{
		{Query: " второй запрос ", Frequency: "1 200"},
		{Query: "первый запрос", Frequency: "2 500"},
		{Query: "второй запрос", Frequency: "900"},
		{Query: "", Frequency: "9999"},
	}
	want := []KeywordFrequency{
		{Query: "первый запрос", Frequency: 2500},
		{Query: "второй запрос", Frequency: 1200},
	}

	got, err := normalizeResults(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeResults() = %#v, want %#v", got, want)
	}
}

func TestUniqueNonEmptyPreservesOrderAndRemovesExactDuplicates(t *testing.T) {
	got := uniqueNonEmpty([]string{" образование ", "", "Профессия", "образование", "ОБРАЗОВАНИЕ"})
	want := []string{"образование", "Профессия", "ОБРАЗОВАНИЕ"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueNonEmpty() = %#v, want %#v", got, want)
	}
}

func TestNormalizeCompetitorStructure(t *testing.T) {
	input := "  H1 Как стать логопедом\r\n\r\n| H2 Обучение\r\r+++ H3 Где учиться\n\n\n+++   |H3 Зарплата  "
	want := "H1 Как стать логопедом\n\nH2 Обучение\n\nH3 Где учиться\n\nH3 Зарплата"
	got := normalizeCompetitorStructure(input)
	if got != want {
		t.Fatalf("normalizeCompetitorStructure() = %q, want %q", got, want)
	}
}

func TestNormalizeResultsLimitsToFifty(t *testing.T) {
	rows := make([]rawKeywordFrequency, 55)
	for index := range rows {
		rows[index] = rawKeywordFrequency{Query: fmt.Sprintf("query-%02d", index), Frequency: fmt.Sprint(index)}
	}
	got, err := normalizeResults(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Fatalf("len(normalizeResults()) = %d, want 50", len(got))
	}
	if got[0].Frequency != 54 || got[49].Frequency != 5 {
		t.Fatalf("unexpected sorted range: first=%d last=%d", got[0].Frequency, got[49].Frequency)
	}
}

func TestNormalizeResultsRejectsInvalidFrequency(t *testing.T) {
	_, err := normalizeResults([]rawKeywordFrequency{{Query: "query", Frequency: "нет данных"}})
	if err == nil {
		t.Fatal("expected invalid frequency error")
	}
}

func TestNormalizeResultsRejectsFrequencyWithUnexpectedText(t *testing.T) {
	_, err := normalizeResults([]rawKeywordFrequency{{Query: "query", Frequency: "100 показов"}})
	if err == nil {
		t.Fatal("expected frequency with text error")
	}
}

func TestNormalizeResultsKeepsStableOrderForEqualFrequency(t *testing.T) {
	rows := []rawKeywordFrequency{
		{Query: "второй по алфавиту", Frequency: "100"},
		{Query: "первый по алфавиту", Frequency: "100"},
	}
	got, err := normalizeResults(rows)
	if err != nil {
		t.Fatal(err)
	}
	want := []KeywordFrequency{
		{Query: "второй по алфавиту", Frequency: 100},
		{Query: "первый по алфавиту", Frequency: 100},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeResults() = %#v, want %#v", got, want)
	}
}

func TestParseWordstatRowsFindsHeadersAndNormalizesResults(t *testing.T) {
	rows := [][]string{
		{"Отчёт Wordstat"},
		{"Фраза", "Весь мир(WS)"},
		{"первый запрос", "2 500"},
		{"второй запрос", "1200"},
	}
	want := []KeywordFrequency{
		{Query: "первый запрос", Frequency: 2500},
		{Query: "второй запрос", Frequency: 1200},
	}
	got, err := parseWordstatRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseWordstatRows() = %#v, want %#v", got, want)
	}
}

func TestParseWordstatRowsRejectsUnknownHeaders(t *testing.T) {
	_, err := parseWordstatRows([][]string{{"foo", "bar"}, {"query", "1"}})
	if err == nil {
		t.Fatal("expected unknown Wordstat headers error")
	}
}

func TestNormalizeInputQueriesUsesOneUnnumberedPhrasePerLine(t *testing.T) {
	got := normalizeInputQueries([]string{" первый запрос ", "", "второй запрос", "первый запрос"})
	want := []string{"первый запрос", "второй запрос"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeInputQueries() = %#v, want %#v", got, want)
	}
}

func TestAcceptCopywritersResultRejectsPreviousTask(t *testing.T) {
	tests := []struct {
		name       string
		previous   copywritersTask
		current    copywritersTask
		wantErr    bool
		wantSubstr string
	}{
		{
			name:     "новая задача принимается",
			previous: copywritersTask{ID: "1001", Theme: "старые слова", Structure: "старая структура"},
			current:  copywritersTask{ID: "1002", Theme: "новые слова", Structure: "новая структура"},
		},
		{
			name:     "первая задача в чистом профиле принимается",
			previous: copywritersTask{},
			current:  copywritersTask{ID: "1002", Theme: "новые слова", Structure: "новая структура"},
		},
		{
			name:       "прежний task_id не принимается",
			previous:   copywritersTask{ID: "1001", Theme: "старые слова", Structure: "старая структура"},
			current:    copywritersTask{ID: "1001", Theme: "старые слова", Structure: "старая структура"},
			wantErr:    true,
			wantSubstr: "не изменился",
		},
		{
			name:       "результат без task_id не принимается",
			previous:   copywritersTask{ID: "1001"},
			current:    copywritersTask{Theme: "слова", Structure: "структура"},
			wantErr:    true,
			wantSubstr: "не выдал task_id",
		},
		{
			name:       "неизменённые данные без task_id не принимаются",
			previous:   copywritersTask{Theme: "слова", Structure: "структура"},
			current:    copywritersTask{ID: "1002", Theme: "слова", Structure: "структура"},
			wantErr:    true,
			wantSubstr: "неизменённый результат",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := acceptCopywritersResult(test.previous, test.current)
			if test.wantErr {
				if err == nil {
					t.Fatal("результат предыдущей задачи принят как новый")
				}
				if !strings.Contains(err.Error(), test.wantSubstr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("новая задача отклонена: %v", err)
			}
		})
	}
}

func TestSelectNewWordstatTaskAcceptsOnlyTheTaskOfThisRun(t *testing.T) {
	tests := []struct {
		name       string
		known      []string
		completed  []string
		want       string
		wantErr    bool
		wantSubstr string
	}{
		{
			name:      "новая задача среди прежних",
			known:     []string{"9001", "9002"},
			completed: []string{"9001", "9002", "9003"},
			want:      "9003",
		},
		{
			name:      "первая задача в чистом профиле",
			known:     nil,
			completed: []string{"9003"},
			want:      "9003",
		},
		{
			name:       "на странице только прежние задачи",
			known:      []string{"9001", "9002"},
			completed:  []string{"9001", "9002"},
			wantErr:    true,
			wantSubstr: "не создал новую задачу",
		},
		{
			name:       "пустой список завершённых задач",
			known:      []string{"9001"},
			completed:  nil,
			wantErr:    true,
			wantSubstr: "не создал новую задачу",
		},
		{
			name:       "две новые задачи неразличимы",
			known:      []string{"9001"},
			completed:  []string{"9001", "9003", "9004"},
			wantErr:    true,
			wantSubstr: "несколько новых задач",
		},
		{
			name:      "повтор одной и той же новой задачи",
			known:     []string{"9001"},
			completed: []string{"9003", "9003"},
			want:      "9003",
		},
		{
			name:      "пробелы вокруг идентификаторов не создают новую задачу",
			known:     []string{" 9001 "},
			completed: []string{"9001", "  ", "9003"},
			want:      "9003",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectNewWordstatTask(test.known, test.completed)
			if test.wantErr {
				if err == nil {
					t.Fatalf("чужая задача принята как новая: %q", got)
				}
				if !strings.Contains(err.Error(), test.wantSubstr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("новая задача отклонена: %v", err)
			}
			if got != test.want {
				t.Fatalf("task_id = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAcceptWordstatResultRejectsForeignTable(t *testing.T) {
	submitted := []string{"как стать логопедом", "логопед обучение", "профессия логопед"}

	if err := acceptWordstatResult(submitted, []KeywordFrequency{
		{Query: "Как стать логопедом", Frequency: 5400},
		{Query: "логопед  обучение", Frequency: 3100},
	}); err != nil {
		t.Fatalf("свой результат отклонён: %v", err)
	}

	err := acceptWordstatResult(submitted, []KeywordFrequency{
		{Query: "такелажник работа", Frequency: 900},
		{Query: "кто такие такелажники", Frequency: 400},
		{Query: "такелажником", Frequency: 200},
	})
	if err == nil {
		t.Fatal("таблица другой задачи принята")
	}
	if !strings.Contains(err.Error(), "результат другой задачи") || !strings.Contains(err.Error(), "такелажник работа") {
		t.Fatalf("error = %v", err)
	}

	if err := acceptWordstatResult(submitted, nil); err != nil {
		t.Fatalf("пустая таблица должна разбираться выше по стеку: %v", err)
	}
}

// fakeWordstatTaskList воспроизводит поведение страницы Arsenkin: список задач меняется
// только при перезагрузке, а часы двигает само ожидание. Так правила подтверждения старта
// проверяются без браузера и без реальных пауз.
type fakeWordstatTaskList struct {
	// renders — состояние списка задач: [0] — то, что видно сразу после клика,
	// каждое следующее — то, что появится после очередной перезагрузки.
	renders [][]string
	// renderDelay — сколько времени списку нужно на отрисовку после загрузки страницы.
	// Ноль означает, что список отрисован сразу.
	renderDelay time.Duration
	// reloadCost — во что обходится перезагрузка страницы; ноль означает секунду.
	reloadCost time.Duration
	index      int
	clock      time.Time
	reloads    int
	// readsBeforeRender считает чтения списка, сделанные до его отрисовки: именно так
	// выглядел прежний дефект — цикл перезагружал страницу, ни разу не увидев задачи.
	readsBeforeRender int
	rendered          bool
	readErr           error
}

func (f *fakeWordstatTaskList) current() []string {
	if !f.rendered {
		return nil
	}
	if f.index >= len(f.renders) {
		return f.renders[len(f.renders)-1]
	}
	return f.renders[f.index]
}

func (f *fakeWordstatTaskList) list() wordstatTaskList {
	return wordstatTaskList{
		waitRendered: func(timeout time.Duration) error {
			if f.renderDelay > timeout {
				// Списку не хватило окна: время вышло, страница так и не отрисовалась.
				f.clock = f.clock.Add(timeout)
				return fmt.Errorf("timeout")
			}
			f.clock = f.clock.Add(f.renderDelay)
			f.rendered = true
			return nil
		},
		taskIDs: func() ([]string, error) {
			if f.readErr != nil {
				return nil, f.readErr
			}
			if !f.rendered {
				f.readsBeforeRender++
			}
			return f.current(), nil
		},
		reload: func() error {
			f.reloads++
			f.index++
			f.rendered = false
			cost := f.reloadCost
			if cost == 0 {
				cost = time.Second
			}
			f.clock = f.clock.Add(cost)
			return nil
		},
		now: func() time.Time { return f.clock },
	}
}

// TestWaitWordstatTaskCreatedSeesTaskOnlyAfterReload закрывает регресс, из-за которого
// prepare зависал на десять минут: список задач Arsenkin отрисовывается при загрузке
// страницы, поэтому в открытом после клика документе новая задача не появляется никогда.
// Ожидание без перезагрузки выбирало весь бюджет и падало по таймауту.
func TestWaitWordstatTaskCreatedSeesTaskOnlyAfterReload(t *testing.T) {
	page := &fakeWordstatTaskList{renders: [][]string{
		{"9001", "9002"},
		{"9001", "9002"},
		{"9001", "9002", "9003"},
	}}

	taskID, err := waitWordstatTaskCreated(context.Background(), []string{"9001", "9002"}, page.list(), nil)
	if err != nil {
		t.Fatalf("новая задача не найдена: %v", err)
	}
	if taskID != "9003" {
		t.Fatalf("task_id = %q, want %q", taskID, "9003")
	}
	if page.reloads == 0 {
		t.Fatal("список задач ни разу не перезагружен: в открытом документе новая задача не появится")
	}
	if page.readsBeforeRender != 0 {
		t.Fatalf("список прочитан %d раз до отрисовки", page.readsBeforeRender)
	}
}

// TestWaitWordstatTaskCreatedToleratesDelayedTask: задача заводится не мгновенно, и
// несколько пустых проверок подряд — не повод объявлять запуск неудачным.
func TestWaitWordstatTaskCreatedToleratesDelayedTask(t *testing.T) {
	renders := [][]string{{"9001"}, {"9001"}, {"9001"}, {"9001"}, {"9001"}, {"9001", "9007"}}
	page := &fakeWordstatTaskList{renders: renders}
	started := page.clock

	taskID, err := waitWordstatTaskCreated(context.Background(), []string{"9001"}, page.list(), nil)
	if err != nil {
		t.Fatalf("задача с задержкой создания отклонена: %v", err)
	}
	if taskID != "9007" {
		t.Fatalf("task_id = %q, want %q", taskID, "9007")
	}
	if elapsed := page.clock.Sub(started); elapsed > wordstatStartTimeout*time.Millisecond {
		t.Fatalf("ожидание вышло за бюджет подтверждения: %s", elapsed)
	}
}

// TestWaitWordstatTaskCreatedRejectsMissingTaskFast — кнопка нажата, но задача не создана.
// Проверяется главное: прежние задачи не принимаются за новую, ошибка называет причину,
// и этап завершается по короткому бюджету подтверждения, а не по общему таймауту результата.
func TestWaitWordstatTaskCreatedRejectsMissingTaskFast(t *testing.T) {
	page := &fakeWordstatTaskList{renders: [][]string{{"9001", "9002"}}}
	started := page.clock

	taskID, err := waitWordstatTaskCreated(context.Background(), []string{"9001", "9002"}, page.list(), nil)
	if err == nil {
		t.Fatalf("несозданная задача принята: %q", taskID)
	}
	if !errors.Is(err, errWordstatTaskNotCreated) {
		t.Fatalf("error = %v, want errWordstatTaskNotCreated", err)
	}
	if !strings.Contains(err.Error(), "не принял запросы") {
		t.Fatalf("ошибка не называет причину: %v", err)
	}
	elapsed := page.clock.Sub(started)
	if elapsed > wordstatStartTimeout*time.Millisecond {
		t.Fatalf("этап ждал дольше бюджета подтверждения: %s", elapsed)
	}
	if elapsed >= wordstatTimeout*time.Millisecond {
		t.Fatalf("этап выбрал общий таймаут результата вместо быстрого отказа: %s", elapsed)
	}
	if page.reloads == 0 {
		t.Fatal("отказ объявлен без единой перезагрузки списка задач")
	}
}

// TestWaitWordstatTaskCreatedRejectsSeveralNewTasks: две новые задачи сразу означают, что
// свою определить нельзя. Повторять проверку бессмысленно — это окончательный отказ.
func TestWaitWordstatTaskCreatedRejectsSeveralNewTasks(t *testing.T) {
	page := &fakeWordstatTaskList{renders: [][]string{{"9001", "9003", "9004"}}}

	taskID, err := waitWordstatTaskCreated(context.Background(), []string{"9001"}, page.list(), nil)
	if err == nil {
		t.Fatalf("неразличимые задачи приняты: %q", taskID)
	}
	if !strings.Contains(err.Error(), "несколько новых задач") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, errWordstatTaskNotCreated) {
		t.Fatalf("отказ выдан за ожидание создания задачи: %v", err)
	}
}

// TestWaitWordstatTaskCreatedAcceptsFirstTaskOfCleanProfile: пустая история — законное
// состояние, и первая же задача в ней своя.
func TestWaitWordstatTaskCreatedAcceptsFirstTaskOfCleanProfile(t *testing.T) {
	page := &fakeWordstatTaskList{renders: [][]string{{}, {"9010"}}}

	taskID, err := waitWordstatTaskCreated(context.Background(), nil, page.list(), nil)
	if err != nil {
		t.Fatalf("первая задача чистого профиля отклонена: %v", err)
	}
	if taskID != "9010" {
		t.Fatalf("task_id = %q, want %q", taskID, "9010")
	}
}

func TestWaitWordstatTaskCreatedStopsOnCancelledContext(t *testing.T) {
	page := &fakeWordstatTaskList{renders: [][]string{{"9001"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := waitWordstatTaskCreated(ctx, []string{"9001"}, page.list(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// TestWaitWordstatTaskCreatedWaitsForRenderedHistoryBeforeReload закрывает дефект прошлого
// цикла: он перезагружал страницу раз в 5 с, а список задач приходит отдельным XHR и не
// всегда успевает отрисоваться. Каждая перезагрузка обрывала запрос, цикл ни разу не видел
// список и заканчивался выводом «задача не создана» — при живой задаче.
func TestWaitWordstatTaskCreatedWaitsForRenderedHistoryBeforeReload(t *testing.T) {
	page := &fakeWordstatTaskList{
		renders:     [][]string{{"9001"}, {"9001", "9005"}},
		renderDelay: 12 * time.Second, // дольше прежних 5 с, но в пределах wordstatHistoryTimeout
	}

	taskID, err := waitWordstatTaskCreated(context.Background(), []string{"9001"}, page.list(), nil)
	if err != nil {
		t.Fatalf("задача не найдена при медленной отрисовке списка: %v", err)
	}
	if taskID != "9005" {
		t.Fatalf("task_id = %q, want %q", taskID, "9005")
	}
	if page.readsBeforeRender != 0 {
		t.Fatalf("список прочитан %d раз до отрисовки: перезагрузка обгоняет XHR истории", page.readsBeforeRender)
	}
}

// TestWaitWordstatTaskCreatedSeparatesUnrenderedHistoryFromMissingTask: если список так и не
// отрисовался, честный ответ — «проверить нечем», а не «Arsenkin не принял запросы».
// Прежняя формулировка приписывала Arsenkin отказ, которого никто не наблюдал.
func TestWaitWordstatTaskCreatedSeparatesUnrenderedHistoryFromMissingTask(t *testing.T) {
	page := &fakeWordstatTaskList{
		renders:     [][]string{{"9001"}},
		renderDelay: (wordstatHistoryTimeout + 5_000) * time.Millisecond,
	}

	_, err := waitWordstatTaskCreated(context.Background(), []string{"9001"}, page.list(), nil)
	if err == nil {
		t.Fatal("неотрисованный список принят за ответ")
	}
	if !strings.Contains(err.Error(), "ни разу не отрисовался") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, errWordstatTaskNotCreated) {
		t.Fatalf("необследованный список выдан за отказ Arsenkin: %v", err)
	}
}

func TestClassifySubmitDistinguishesThreeOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		outcome submitOutcome
		want    string
	}{
		{"POST не стартовал", submitOutcome{}, submitVerdictNotStarted},
		{"обработчик упал до запроса", submitOutcome{PageErrors: []string{"TypeError"}}, submitVerdictNotStarted},
		{"страница подтвердила отправку без сетевого события", submitOutcome{Acknowledged: true}, submitVerdictAccepted},
		{"сервер отклонил", submitOutcome{RequestStarted: true, ResponseStatus: 500}, submitVerdictRejected},
		{"доступ закрыт", submitOutcome{RequestStarted: true, ResponseStatus: 403}, submitVerdictRejected},
		{"принят", submitOutcome{RequestStarted: true, ResponseStatus: 200}, submitVerdictAccepted},
		{"принят, ответ ещё не пришёл", submitOutcome{RequestStarted: true}, submitVerdictAccepted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifySubmit(test.outcome); got != test.want {
				t.Fatalf("classifySubmit() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestIsWordstatSubmitRequest: GET той же страницы — не отправка. Иначе обычная навигация
// сошла бы за доказательство приёма запросов.
func TestIsWordstatSubmitRequest(t *testing.T) {
	tests := []struct {
		method, url string
		want        bool
	}{
		{"POST", "https://arsenkin.ru/tools/wordstat/", true},
		{"post", "https://arsenkin.ru/tools/wordstat/", true},
		{"GET", "https://arsenkin.ru/tools/wordstat/", false},
		{"POST", "https://arsenkin.ru/tools/history/", false},
		{"POST", "https://arsenkin.ru/tools/getlimits/", false},
	}
	for _, test := range tests {
		if got := isWordstatSubmitRequest(test.method, test.url); got != test.want {
			t.Fatalf("isWordstatSubmitRequest(%q, %q) = %t, want %t", test.method, test.url, got, test.want)
		}
	}
}

func TestWordstatInputStateAcceptsOnlyUsableField(t *testing.T) {
	queries := []string{"первый запрос", "второй запрос"}
	expected := strings.Join(queries, "\n")
	good := wordstatInputState{
		Selector: keysSelector, QueriesCount: len(queries),
		ExpectedLength: len([]rune(expected)), ExpectedLines: 2, ExpectedFingerprint: fingerprint(expected),
		DOMLength: len([]rune(expected)), DOMLines: 2, DOMFingerprint: fingerprint(expected),
		Visible: true, Enabled: true,
	}
	if err := good.accept(); err != nil {
		t.Fatalf("корректно заполненное поле отклонено: %v", err)
	}
	if !good.Match() {
		t.Fatal("совпавшие отпечатки не признаны совпадением")
	}

	empty := good
	empty.DOMLength, empty.DOMLines, empty.DOMFingerprint = 0, 0, fingerprint("")
	if err := empty.accept(); err == nil || !strings.Contains(err.Error(), "пустое") {
		t.Fatalf("пустое поле принято: %v", err)
	}

	truncated := good
	truncated.DOMLines, truncated.DOMFingerprint = 1, fingerprint("первый запрос")
	if err := truncated.accept(); err == nil || !strings.Contains(err.Error(), "строк") {
		t.Fatalf("обрезанный ввод принят: %v", err)
	}

	readOnly := good
	readOnly.ReadOnly = true
	if err := readOnly.accept(); err == nil || !strings.Contains(err.Error(), "недоступно") {
		t.Fatalf("недоступное поле принято: %v", err)
	}

	// Страница нормализует переводы строк на change: расхождение отпечатков при том же
	// числе строк не должно останавливать прогон, иначе доказать отправку нечем.
	normalized := good
	normalized.DOMFingerprint = fingerprint(expected + " ")
	if err := normalized.accept(); err != nil {
		t.Fatalf("нормализованный страницей ввод отклонён: %v", err)
	}
	if normalized.Match() {
		t.Fatal("расхождение отпечатков выдано за совпадение")
	}
}

// TestFingerprintIdentifiesWithoutDisclosing: отпечаток различает тексты и не является самим
// текстом — ключевые слова в журнал и артефакты не попадают.
func TestFingerprintIdentifiesWithoutDisclosing(t *testing.T) {
	value := "перенос независимой оценки квалификации\nсроки прохождения нок"
	got := fingerprint(value)
	if len(got) != 12 {
		t.Fatalf("len(fingerprint()) = %d, want 12", len(got))
	}
	if got != fingerprint(value) {
		t.Fatal("отпечаток неустойчив")
	}
	if got == fingerprint(value+"!") {
		t.Fatal("разные тексты дали одинаковый отпечаток")
	}
	if strings.Contains(got, "нок") || strings.Contains(value, got) {
		t.Fatalf("отпечаток раскрывает исходный текст: %q", got)
	}
}

func TestCountLinesAndTruncateRunes(t *testing.T) {
	if got := countLines(""); got != 0 {
		t.Fatalf("countLines(\"\") = %d, want 0", got)
	}
	if got := countLines("одна"); got != 1 {
		t.Fatalf("countLines(одна) = %d, want 1", got)
	}
	if got := countLines("одна\nдве\nтри"); got != 3 {
		t.Fatalf("countLines = %d, want 3", got)
	}
	if got := truncateRunes("  запрос  ", 100); got != "запрос" {
		t.Fatalf("truncateRunes() = %q", got)
	}
	got := truncateRunes("ключевая фраза", 8)
	if []rune(got)[8] != '…' || len([]rune(got)) != 9 {
		t.Fatalf("truncateRunes() = %q, want 8 рун и многоточие", got)
	}
}

// TestLimitWordstatQueriesKeepsFirstPhrasesInOrder: форма Wordstat принимает ограниченный
// список, поэтому длинный обрезается с конца — порядок первых запросов не меняется, иначе
// в отчёт уехали бы не те фразы, что запрашивались.
func TestLimitWordstatQueriesKeepsFirstPhrasesInOrder(t *testing.T) {
	queries := func(count int) []string {
		result := make([]string, count)
		for index := range result {
			result[index] = fmt.Sprintf("запрос-%03d", index)
		}
		return result
	}

	tests := []struct {
		name      string
		input     []string
		wantCount int
	}{
		{"меньше лимита", queries(maxWordstatQueries - 1), maxWordstatQueries - 1},
		{"ровно лимит", queries(maxWordstatQueries), maxWordstatQueries},
		{"больше лимита", queries(maxWordstatQueries + 6), maxWordstatQueries},
		{"вдвое больше лимита", queries(maxWordstatQueries * 2), maxWordstatQueries},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := limitWordstatQueries(test.input)
			if len(got) != test.wantCount {
				t.Fatalf("len(limitWordstatQueries()) = %d, want %d", len(got), test.wantCount)
			}
			if !reflect.DeepEqual(got, test.input[:test.wantCount]) {
				t.Fatalf("порядок или состав запросов изменился: %v", got)
			}
		})
	}
}

// TestLimitWordstatQueriesLeavesShortListUntouched фиксирует, что для непревышающего списка
// возвращается он сам: обрезка не должна трогать ни ручные запросы, ни результат Keys.so.
func TestLimitWordstatQueriesLeavesShortListUntouched(t *testing.T) {
	input := []string{"сроки прохождения нок", "нок для нрс", "когда проходить нок"}
	got := limitWordstatQueries(input)
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("limitWordstatQueries() = %v, want %v", got, input)
	}
	if len(input) >= maxWordstatQueries {
		t.Fatalf("проверка потеряла смысл: список из %d запросов не короче лимита %d", len(input), maxWordstatQueries)
	}
}

// TestLimitWordstatQueriesKeepsFingerprintOfSubmittedText: отпечаток поля считается по тому
// же тексту, что уходит в форму, — обрезка не должна порождать расхождения ожидаемого и DOM.
func TestLimitWordstatQueriesKeepsFingerprintOfSubmittedText(t *testing.T) {
	long := make([]string, maxWordstatQueries+10)
	for index := range long {
		long[index] = fmt.Sprintf("фраза-%03d", index)
	}
	submitted := limitWordstatQueries(long)
	if fingerprint(strings.Join(submitted, "\n")) == fingerprint(strings.Join(long, "\n")) {
		t.Fatal("отпечатки полного и обрезанного списков совпали")
	}
	if countLines(strings.Join(submitted, "\n")) != maxWordstatQueries {
		t.Fatalf("в форму уходит %d строк вместо %d", countLines(strings.Join(submitted, "\n")), maxWordstatQueries)
	}
}

// TestSubmittedQueriesMatchesWhatCollectResearchSends: диагностика в run.go спрашивает
// отправляемый набор у клиента, поэтому SubmittedQueries обязан повторять то, что делает
// CollectResearch перед заполнением формы, — нормализацию и обрезку по лимиту.
func TestSubmittedQueriesMatchesWhatCollectResearchSends(t *testing.T) {
	raw := []string{" бариста обучение ", "", "бариста обучение", "работа бариста"}
	for index := 0; index < maxWordstatQueries+11; index++ {
		raw = append(raw, fmt.Sprintf("бариста запрос %02d", index))
	}

	got := SubmittedQueries(raw)
	want := limitWordstatQueries(normalizeInputQueries(raw))
	if !reflect.DeepEqual(got, want) {
		t.Fatal("SubmittedQueries разошёлся с парой normalizeInputQueries + limitWordstatQueries")
	}
	if len(got) != maxWordstatQueries {
		t.Fatalf("len(SubmittedQueries()) = %d, want %d", len(got), maxWordstatQueries)
	}
	if got[0] != "бариста обучение" || got[1] != "работа бариста" {
		t.Fatalf("нормализация или порядок нарушены: %v", got[:2])
	}
}

func TestSubmittedQueriesLeavesShortListNormalizedOnly(t *testing.T) {
	raw := []string{" первый ", "второй", "первый"}
	got := SubmittedQueries(raw)
	want := []string{"первый", "второй"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SubmittedQueries() = %v, want %v", got, want)
	}
}

// TestWordstatProgressReporterReportsEachThresholdOnce фиксирует поведение отчётности о
// прогрессе Wordstat: каждая ступень сообщается ровно один раз, повторные чтения того же
// процента молчат, а перескок через ступени сообщает все пройденные.
func TestWordstatProgressReporterReportsEachThresholdOnce(t *testing.T) {
	var reporter wordstatProgressReporter

	if crossed := reporter.crossed(10); len(crossed) != 0 {
		t.Fatalf("до первой ступени сообщено %v", crossed)
	}
	if crossed := reporter.crossed(30); !reflect.DeepEqual(crossed, []int{25}) {
		t.Fatalf("на 30%% сообщено %v, want [25]", crossed)
	}
	if crossed := reporter.crossed(30); len(crossed) != 0 {
		t.Fatalf("повторное чтение того же процента сообщило %v", crossed)
	}
	if crossed := reporter.crossed(80); !reflect.DeepEqual(crossed, []int{50, 75}) {
		t.Fatalf("на 80%% сообщено %v, want [50 75]", crossed)
	}
	if crossed := reporter.crossed(100); len(crossed) != 0 {
		t.Fatalf("после последней ступени сообщено %v", crossed)
	}
}

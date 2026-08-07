package deepseekweb

import (
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var jsOptionRE = regexp.MustCompile(`options\.([A-Za-z_][A-Za-z0-9_]*)`)

func jsOptionNames(script string) []string {
	seen := map[string]struct{}{}
	for _, match := range jsOptionRE.FindAllStringSubmatch(script, -1) {
		seen[match[1]] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Расхождение имён между Go и JS ничем не проявляется в рантайме: сравнение просто идёт с
// undefined и всегда ложно. Именно так «не менялся stableForMs» превратилось бы в «не
// менялся undefined».
func TestCompletedAnswerOptionsMatchScript(t *testing.T) {
	passed := completedAnswerOptions(answerMark{})
	goNames := make([]string, 0, len(passed))
	for name := range passed {
		goNames = append(goNames, name)
	}
	sort.Strings(goNames)

	scriptNames := jsOptionNames(completedAnswerJS)
	if strings.Join(goNames, ",") != strings.Join(scriptNames, ",") {
		t.Fatalf("Go передаёт %v, скрипт читает %v", goNames, scriptNames)
	}
}

func TestResponseStateOptionsMatchScript(t *testing.T) {
	passed := responseStateOptions(answerMark{})
	goNames := make([]string, 0, len(passed))
	for name := range passed {
		goNames = append(goNames, name)
	}
	sort.Strings(goNames)
	if got := jsOptionNames(responseStateJS); strings.Join(got, ",") != strings.Join(goNames, ",") {
		t.Fatalf("скрипт читает %v, Go передаёт %v", got, goNames)
	}
}

// Тред виртуализирован: старые сообщения размонтируются, поэтому число ответов на странице
// не растёт. Прогон статьи 47 вставал ровно на этом — ответ на info существовал в DOM
// элементом key=6, а счётчик показывал прежние 2.
func TestFreshAnswerIsFoundByItemKeyNotByCount(t *testing.T) {
	if !strings.Contains(freshAnswerJS, "options.itemKeyAttribute") {
		t.Fatal("новый ответ ищется без опоры на ключ элемента")
	}
	if !strings.Contains(freshAnswerJS, "key <= options.previousKey") {
		t.Fatal("нет границы «нового» по ключу")
	}
	if strings.Contains(completedAnswerJS, "answers.length <= options.previousCount") {
		t.Fatal("вернулась проверка завершения по количеству ответов")
	}
	// Запасной путь по количеству остаётся на случай смены разметки списка.
	if !strings.Contains(freshAnswerJS, "items.length === 0") {
		t.Fatal("потерян запасной путь для невиртуализированного треда")
	}
}

// Оба скрипта ожидания обязаны искать новый ответ одним и тем же правилом, иначе
// completedAnswerJS и responseStateJS разойдутся и лог начнёт врать.
func TestBothWaitScriptsShareTheSameFreshAnswerRule(t *testing.T) {
	for name, script := range map[string]string{"completedAnswerJS": completedAnswerJS, "responseStateJS": responseStateJS} {
		if !strings.Contains(script, "findFreshAnswer") {
			t.Fatalf("%s не использует общее правило поиска нового ответа", name)
		}
	}
}

func TestLastItemKeyScriptOptionsMatchGo(t *testing.T) {
	want := []string{"itemKeyAttribute", "itemSelector"}
	if got := jsOptionNames(lastItemKeyJS); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("скрипт читает %v, Go передаёт %v", got, want)
	}
}

// Пустой снимок обязан означать «любой ответ — новый»: на первом сообщении тред пуст.
func TestEmptyMarkAcceptsAnyAnswer(t *testing.T) {
	mark := answerMark{count: 0, key: -1}
	options := responseStateOptions(mark)
	if options["previousKey"] != -1 {
		t.Fatalf("previousKey = %v, want -1", options["previousKey"])
	}
	if options["previousCount"] != 0 {
		t.Fatalf("previousCount = %v, want 0", options["previousCount"])
	}
}

// Основной признак завершения — панель действий под ответом. Она отрисовывается только
// после конца генерации, в отличие от кнопки остановки, которую stopSelector не находит:
// у кнопок DeepSeek нет ни aria-label, ни title.
func TestCompletionRequiresActionPanelOrLongStability(t *testing.T) {
	for _, fragment := range []string{
		`querySelectorAll('[role="button"]')`,
		"getBoundingClientRect().top >= answerBottom - 4",
		"if (actionsReady) return now - state.changedAt >= options.settledForMs;",
		"return now - state.changedAt >= options.stableForMs;",
	} {
		if !strings.Contains(completedAnswerJS, fragment) {
			t.Fatalf("проверка завершения потеряла фрагмент %q", fragment)
		}
	}
}

func TestStabilityWindowIsLongEnoughWithoutActionPanel(t *testing.T) {
	if responseSettledFor >= responseStableFor {
		t.Fatalf("settled %v должен быть меньше stable %v", responseSettledFor, responseStableFor)
	}
	// Прежние 4 s принимали обычную паузу стрима за конец ответа: статья 47 сохранилась
	// обрезанной на полуслове. Запасное окно обязано быть заметно больше такой паузы.
	if responseStableFor < 15*time.Second {
		t.Fatalf("stable = %v, слишком мало для запасного признака", responseStableFor)
	}
	if responseSettledFor < time.Second {
		t.Fatalf("settled = %v, ответ будет читаться в момент отрисовки панели", responseSettledFor)
	}
}

// Ожидание ответа обязано укладываться в таймауты стадий: запасное окно прибавляется к
// каждой генерации, и если оно сопоставимо с таймаутом, стадия начнёт падать сама по себе.
func TestStabilityWindowFitsIntoShortestStageTimeout(t *testing.T) {
	const shortestStageTimeout = 120 * time.Second
	if responseStableFor > shortestStageTimeout/4 {
		t.Fatalf("stable = %v против самого короткого таймаута стадии %v", responseStableFor, shortestStageTimeout)
	}
}

func TestSendButtonScriptOptionsMatchGo(t *testing.T) {
	if got := jsOptionNames(clickSendButtonJS); strings.Join(got, ",") != "composerSelector" {
		t.Fatalf("скрипт читает %v, Go передаёт composerSelector", got)
	}
}

// Подтверждение отправки должно укладываться в разумную долю самой короткой стадии:
// иначе неудачная отправка съест бюджет, ради экономии которого проверка и вводилась.
func TestPromptSentTimeoutIsShort(t *testing.T) {
	if promptSentTimeout > 30*time.Second {
		t.Fatalf("promptSentTimeout = %v, слишком долго для проверки отправки", promptSentTimeout)
	}
	if promptPollInterval >= promptSentTimeout {
		t.Fatalf("интервал опроса %v не меньше таймаута %v", promptPollInterval, promptSentTimeout)
	}
}

func TestBlockedStateOptionsMatchScript(t *testing.T) {
	passed := blockedStateOptions()
	goNames := make([]string, 0, len(passed))
	for name := range passed {
		goNames = append(goNames, name)
	}
	sort.Strings(goNames)
	if got := jsOptionNames(blockedStateJS); strings.Join(got, ",") != strings.Join(goNames, ",") {
		t.Fatalf("скрипт читает %v, Go передаёт %v", got, goNames)
	}
}

// Заглушка Cloudflare приходит и на рабочей странице чата, где поле ввода на месте.
// Прогон статьи 47 упирался ровно в это: страница заканчивалась текстом «One more step
// before you proceed…», а мы пять минут ждали ответ.
func TestBlockDetectionCoversCloudflareInterstitial(t *testing.T) {
	for _, marker := range []string{"one more step before you proceed", "verify you are human", "unusual traffic"} {
		if !strings.Contains(blockedStateJS, marker) {
			t.Fatalf("маркер %q потерян", marker)
		}
	}
	if strings.Contains(blockedStateJS, "options.composerSelector") {
		t.Fatal("наличие поля ввода снова считается признаком исправной страницы")
	}
	if !strings.Contains(blockedStateJS, "page.split(answerText).join") {
		t.Fatal("текст ответов не вырезается: слова модели будут приняты за блокировку")
	}
}

package articleaudit

import (
	"errors"
	"strings"
	"testing"
)

// fullAnswer — ответ строго по формату промпта: оценка и два раздела, больше ничего.
const fullAnswer = `Итоговая оценка: 14/20

1. Критические ошибки
Ошибка: на странице нет блока о документе
Почему это проблема: человек не понимает, что получит на руки
Как исправить: добавить раздел о документе после программы обучения

Ошибка: заявлена рабочая профессия, а страница про логопеда
Почему это проблема: читатель не верит остальному тексту
Как исправить: привести плашки text_spec к профессиональной переподготовке

2. Все найденные ошибки списком
По содержанию:
— «В современном мире» в первом абзаце
— обещание «высокий спрос» без цифр
По оформлению:
— абзац на семь строк в разделе о программе
По полям записи:
— text_spec_1 — про рабочую профессию, а страница про логопеда`

func TestParseAnswerReadsScoreAndSections(t *testing.T) {
	parsed, err := ParseAnswer(fullAnswer)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if !parsed.ScoreFound || parsed.Score != 14 || parsed.ScoreMax != 20 {
		t.Fatalf("оценка разобрана как %d/%d (найдена=%v)", parsed.Score, parsed.ScoreMax, parsed.ScoreFound)
	}
	// Содержимое разделов уходит в отчёт дословно.
	critical := parsed.Section(SectionCritical)
	if !strings.HasPrefix(critical, "Ошибка: на странице нет блока о документе") ||
		!strings.Contains(critical, "заявлена рабочая профессия") {
		t.Fatalf("раздел критических ошибок разобран как %q", critical)
	}
	if !strings.Contains(parsed.Section(SectionIssues), "text_spec_1") {
		t.Fatalf("находка по полям потеряна: %q", parsed.Section(SectionIssues))
	}
	if parsed.Unparsed != "" {
		t.Fatalf("ответ по формату дал остаток %q", parsed.Unparsed)
	}
	if got := CountIssues(parsed.Section(SectionIssues)); got != 4 {
		t.Fatalf("находок насчитано %d, ожидалось 4", got)
	}
}

// Заголовок с другой пунктуацией, регистром, номером и разметкой — тот же самый заголовок.
// Номер модель переносит из привычного ей порядка, и терять из-за цифры целый раздел нельзя.
func TestParseAnswerFindsAnchorsLeniently(t *testing.T) {
	answer := `### 4. КРИТИЧЕСКИЕ ОШИБКИ (ТОП-3):
первая находка

**Все найденные ошибки списком**
вторая находка`
	parsed, err := ParseAnswer(answer)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if parsed.Section(SectionCritical) != "первая находка" {
		t.Fatalf("раздел под чужим номером не найден: %q", parsed.Section(SectionCritical))
	}
	if parsed.Section(SectionIssues) != "вторая находка" {
		t.Fatalf("раздел без номера не найден: %q", parsed.Section(SectionIssues))
	}
}

// Разделы, которых промпт больше не просит, модель всё равно пишет — по привычке и потому,
// что прежние версии промпта их требовали. Такой раздел обязан уйти в «Не разобрано»
// целиком, а не подмешаться в список ошибок: иначе человек читал бы в перечне находок
// пересказ критериев оценки.
func TestParseAnswerMovesStraySectionsToUnparsed(t *testing.T) {
	answer := `Итоговая оценка: 12/20

1. Критические ошибки
нет блока о документе

2. Все найденные ошибки списком
— «В современном мире» в первом абзаце

3. Рекомендации
переписать первый абзац так: ...

4. Разбор по критериям
* Структура: 3/4 — комментарий`
	parsed, err := ParseAnswer(answer)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if got := parsed.Section(SectionIssues); got != "— «В современном мире» в первом абзаце" {
		t.Fatalf("в список ошибок подмешался чужой раздел: %q", got)
	}
	for _, want := range []string{"Рекомендации", "переписать первый абзац", "Разбор по критериям", "Структура: 3/4"} {
		if !strings.Contains(parsed.Unparsed, want) {
			t.Fatalf("чужой раздел потерян, в «Не разобрано» нет %q: %q", want, parsed.Unparsed)
		}
	}
}

// Пропущенный раздел — не отказ: он просто пуст, и отчёт скажет об этом словами.
func TestParseAnswerAllowsMissingSection(t *testing.T) {
	parsed, err := ParseAnswer("Итоговая оценка: 19/20\n\n1. Критические ошибки\nзамечаний нет")
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if parsed.Section(SectionIssues) != "" {
		t.Fatal("ненайденный раздел придуман")
	}
	if got := CountIssues(parsed.Section(SectionIssues)); got != 0 {
		t.Fatalf("у пустого раздела насчитано %d находок", got)
	}
}

// Хвост, не легший ни в один якорь, обязан попасть в отчёт целиком: за него заплачено.
func TestParseAnswerKeepsUnanchoredText(t *testing.T) {
	answer := `Разбор страницы про логопеда, вот что вышло.

1. Критические ошибки
нет блока о документе`
	parsed, err := ParseAnswer(answer)
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if !strings.Contains(parsed.Unparsed, "Разбор страницы про логопеда") {
		t.Fatalf("хвост потерян: %q", parsed.Unparsed)
	}
	if parsed.Section(SectionCritical) != "нет блока о документе" {
		t.Fatalf("раздел после хвоста разобран как %q", parsed.Section(SectionCritical))
	}
}

// Ответа нет — отказ стадии: отчёт ни о чём хуже, чем его отсутствие.
func TestParseAnswerRejectsEmpty(t *testing.T) {
	for _, answer := range []string{"", "   \n\t\n"} {
		if _, err := ParseAnswer(answer); !errors.Is(err, ErrEmptyAnswer) {
			t.Fatalf("пустой ответ %q принят: %v", answer, err)
		}
	}
}

// Оценки в ответе нет — так и записываем. Подставить ноль нельзя: по оценке сортируют пачку,
// и «не разобрано» превратилось бы в «страница никуда не годится».
func TestParseAnswerReportsMissingScore(t *testing.T) {
	parsed, err := ParseAnswer("1. Критические ошибки\nнет блока о документе")
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if parsed.ScoreFound || parsed.Score != 0 {
		t.Fatalf("оценка придумана: %d (найдена=%v)", parsed.Score, parsed.ScoreFound)
	}
}

// Оценку модель нередко ставит не в шапку, а в конец ответа. Искать её надо везде.
func TestParseAnswerFindsScoreAnywhere(t *testing.T) {
	parsed, err := ParseAnswer("1. Критические ошибки\nнет блока\n\nИтоговая оценка: 17/20")
	if err != nil {
		t.Fatalf("ParseAnswer: %v", err)
	}
	if !parsed.ScoreFound || parsed.Score != 17 || parsed.ScoreMax != 20 {
		t.Fatalf("оценка в конце ответа не найдена: %d/%d", parsed.Score, parsed.ScoreMax)
	}
	// Строку оценки при этом не тащим в «Не разобрано»: она разобрана и напечатана отдельно.
	if strings.Contains(parsed.Unparsed, "Итоговая оценка") {
		t.Fatalf("строка оценки попала в остаток: %q", parsed.Unparsed)
	}
}

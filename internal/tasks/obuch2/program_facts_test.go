package obuch2

import (
	"strings"
	"testing"
)

// Страница, написанная по книге: объём, срок и цена названы теми же числами, что уйдут в поля
// записи, а в таблице заработка стоят совсем другие суммы — доход по разрядам.
const factsMarkup = `<p>Срок: 150 часов, от 2 недель. Цена: от 5 000 ₽.</p>` +
	`<h2 class="wp-block-heading">Детальная программа обучения</h2>` +
	`<ol class="wp-block-list">` +
	`<li><strong>Модуль 1. Устройство крана — 100 часов.</strong> Механизмы подъёма и поворота.</li>` +
	`<li><strong>Модуль 2. Итоговая аттестация — 50 часов.</strong> Квалификационный экзамен.</li>` +
	`</ol>` +
	`<h2 class="wp-block-heading">Уровень дохода</h2>` +
	`<figure class="wp-block-table"><table><thead><tr><th>Разряд</th><th>Доход</th><th>Где</th></tr></thead>` +
	`<tbody><tr><td>4-й</td><td>70 000 — 120 000 руб.</td><td>Стройплощадки</td></tr></tbody></table></figure>`

var factsBook = ProgramFacts{Hours: "150 часов", Duration: "от 2 недель", Price: "от 5 000 ₽"}

func TestCheckProgramFactsAcceptsPageWrittenByTheBook(t *testing.T) {
	issues := CheckProgramFacts(factsMarkup, factsBook, ParseModules(factsMarkup))

	if len(issues) > 0 {
		t.Fatalf("страница по книге объявлена расходящейся: %v", issues)
	}
}

// Ровно то расхождение, что живёт на живых страницах площадки: в плашке темы одно число, в
// тексте другое. Поймать его можно только здесь — в админке эти два числа не встречаются.
func TestCheckProgramFactsFindsForeignHours(t *testing.T) {
	markup := factsMarkup + `<p>Интенсивная программа объёмом 144 часа готовит к экзамену.</p>`

	issues := CheckProgramFacts(markup, factsBook, ParseModules(markup))

	if len(issues) != 1 || !strings.Contains(issues[0], "144") {
		t.Fatalf("расхождение по часам не найдено: %v", issues)
	}
}

// Часы модулей — те же часы программы, названные по частям: чужими они не считаются.
func TestCheckProgramFactsAllowsModuleHours(t *testing.T) {
	for _, issue := range CheckProgramFacts(factsMarkup, factsBook, ParseModules(factsMarkup)) {
		if strings.Contains(issue, "100") || strings.Contains(issue, "50") {
			t.Fatalf("часы модуля объявлены чужим числом: %s", issue)
		}
	}
}

// Ритм занятий — про расписание, а не про объём: «по 4 часа в день» совпадать с книгой не
// обязано, и ронять им публикацию нельзя.
func TestCheckProgramFactsIgnoresPace(t *testing.T) {
	markup := factsMarkup + `<p>Заниматься достаточно 4 часа в день.</p>`

	if issues := CheckProgramFacts(markup, factsBook, ParseModules(markup)); len(issues) > 0 {
		t.Fatalf("ритм занятий принят за объём программы: %v", issues)
	}
}

// Доход по разрядам — не стоимость курса, и его таблица в сверку не идёт. Отличает её шапка:
// у плашки параметров её нет.
func TestCheckProgramFactsSkipsSalaryTable(t *testing.T) {
	for _, issue := range CheckProgramFacts(factsMarkup, factsBook, nil) {
		if strings.Contains(issue, "70 000") || strings.Contains(issue, "120000") {
			t.Fatalf("доход по разрядам принят за цену курса: %s", issue)
		}
	}
}

// «Возраст от 18 лет» — требование к слушателю, а не срок обучения. Оно стоит почти на каждой
// странице площадки, и ронять им публикацию нельзя.
func TestCheckProgramFactsIgnoresAgeRequirement(t *testing.T) {
	markup := factsMarkup + `<p>Требования: образование не ниже основного общего, возраст от 18 лет.</p>`

	if issues := CheckProgramFacts(markup, factsBook, ParseModules(markup)); len(issues) > 0 {
		t.Fatalf("требование к возрасту принято за срок обучения: %v", issues)
	}
}

func TestCheckProgramFactsFindsForeignPrice(t *testing.T) {
	markup := factsMarkup + `<p>Стоимость обучения — от 7 900 ₽ при оплате одним платежом.</p>`

	issues := CheckProgramFacts(markup, factsBook, ParseModules(markup))

	if len(issues) != 1 || !strings.Contains(issues[0], "7900") {
		t.Fatalf("расхождение по цене не найдено: %v", issues)
	}
}

func TestCheckProgramFactsFindsForeignTerm(t *testing.T) {
	markup := factsMarkup + `<p>Курс занимает от 3 месяцев вечерних занятий.</p>`

	issues := CheckProgramFacts(markup, factsBook, ParseModules(markup))

	if len(issues) != 1 || !strings.Contains(issues[0], "от 3 месяцев") {
		t.Fatalf("расхождение по сроку не найдено: %v", issues)
	}
}

// Пустая колонка книги выключает свою проверку: первые страницы задачи писались, когда
// колонок не было вовсе, и требовать по ним сверки задним числом нельзя.
func TestCheckProgramFactsSkipsEmptyBookColumns(t *testing.T) {
	markup := `<p>Срок: 144 часа, от 3 недель. Цена: от 9 000 ₽.</p>`

	if issues := CheckProgramFacts(markup, ProgramFacts{}, nil); len(issues) > 0 {
		t.Fatalf("пустая книга дала расхождения: %v", issues)
	}
}

// Страница повышения квалификации: площадка подписывает селект «Итоговое тестирование», и
// книга обязана говорить о том же. «Квалификационный экзамен» здесь — расхождение, которое
// на странице видно глазами, а в админке не видно вовсе.
func TestCheckProgramPresetFindsForeignAttestation(t *testing.T) {
	preset, _ := ProgramPresetOf("povyshenie")

	issues := CheckProgramPreset(preset, ProgramFacts{
		Document:    "Удостоверение о повышении квалификации «Агрономия»",
		Attestation: "Квалификационный экзамен",
	})

	if len(issues) != 1 || !strings.Contains(issues[0], "аттестация") {
		t.Fatalf("расхождение по аттестации не найдено: %v", issues)
	}
}

// Книга перечисляет документы через запятую, и требовать от неё точной формы нельзя: селект
// узнаётся по слову.
func TestCheckProgramPresetAcceptsListedDocuments(t *testing.T) {
	preset, _ := ProgramPresetOf("rabprof")

	issues := CheckProgramPreset(preset, ProgramFacts{
		Document:    "Свидетельство о профессии рабочего «Машинист автомобильного крана», удостоверение",
		Attestation: "Квалификационный экзамен",
	})

	if len(issues) > 0 {
		t.Fatalf("книга по площадке объявлена расходящейся: %v", issues)
	}
}

// Документ не того вида — опечатка в типе записи: страница уйдёт рабочей профессией, а
// документ у неё от повышения квалификации.
func TestCheckProgramPresetFindsForeignDocument(t *testing.T) {
	preset, _ := ProgramPresetOf("rabprof")

	issues := CheckProgramPreset(preset, ProgramFacts{
		Document:    "Удостоверение о повышении квалификации",
		Attestation: "Квалификационный экзамен",
	})

	if len(issues) != 1 || !strings.Contains(issues[0], "документ") {
		t.Fatalf("расхождение по документу не найдено: %v", issues)
	}
}

// Незамеренный тип записи пресета не имеет, и поля по нему не уходят вовсе — сверять нечего.
func TestCheckProgramPresetSkipsUnknownPostType(t *testing.T) {
	preset, _ := ProgramPresetOf("obuch_med")

	if issues := CheckProgramPreset(preset, ProgramFacts{
		Document: "Диплом", Attestation: "Собеседование",
	}); len(issues) > 0 {
		t.Fatalf("незамеренный тип дал расхождения: %v", issues)
	}
}

package obuch2

import (
	"strings"
	"testing"
)

// Страница, написанная по книге: объём и срок названы теми же числами, что уйдут в поля
// записи, цены нет вовсе, а в таблице заработка стоят совсем другие суммы — доход по разрядам.
const factsMarkup = `<p>Срок: 150 часов, от 2 недель.</p>` +
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

// The book's own price is an issue too: price lives in the record field only.
func TestCheckProgramFactsFindsBookPriceInText(t *testing.T) {
	markup := factsMarkup + `<p>Стоимость обучения — от 5 000 ₽.</p>`

	issues := CheckProgramFacts(markup, factsBook, ParseModules(markup))

	if len(issues) != 1 || !strings.Contains(issues[0], "5000") {
		t.Fatalf("цена из книги в тексте не найдена: %v", issues)
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

func TestCheckProgramFactsFindsCapitalizedForeignTerm(t *testing.T) {
	markup := factsMarkup + `<p>От 5 недель длится обучение.</p>`

	issues := CheckProgramFacts(markup, factsBook, ParseModules(markup))

	if len(issues) != 1 || !strings.Contains(issues[0], "От 5 недель") {
		t.Fatalf("расхождение по сроку в начале предложения не найдено: %v", issues)
	}
}

// Earnings wording far from the amount, but in the same sentence, is still earnings.
func TestCheckProgramFactsSkipsDistantEarningsWording(t *testing.T) {
	markup := `<p>Зарплата опытного специалиста на крупных строительных объектах Москвы и области начинается от 90 000 рублей.</p>`
	if issues := CheckProgramFacts(markup, factsBook, nil); len(issues) > 0 {
		t.Fatalf("earnings in a long sentence were checked as price: %v", issues)
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

// Оформление блоков пересобирает шапку таблицы заработка обычной строкой, без <thead>.
// Доход в ней — не цена курса, а цена в одноколоночной плашке параметров ловится.
func TestCheckProgramFactsSkipsDecoratedSalaryTable(t *testing.T) {
	markup := `<table><tbody><tr><td><strong>Разряд</strong></td><td><strong>Доход</strong></td></tr>` +
		`<tr><td>4 разряд</td><td>от 70 000 до 100 000 ₽</td></tr></tbody></table>` +
		`<table><tbody><tr><td><strong>Срок:</strong> 150 часов.</td></tr></tbody></table>`
	if issues := CheckProgramFacts(markup, factsBook, nil); len(issues) > 0 {
		t.Fatalf("salary table without thead was checked as price: %v", issues)
	}
	wrong := `<table><tbody><tr><td><strong>Цена:</strong> от 9 000 ₽.</td></tr></tbody></table>`
	if issues := CheckProgramFacts(wrong, factsBook, nil); len(issues) == 0 {
		t.Fatal("one-column parameter table with a wrong price passed the check")
	}
}

// Доход в прозе — не цена курса; цена курса в той же странице ловится.
func TestCheckProgramFactsSkipsEarningsInProse(t *testing.T) {
	markup := `<p>Спрос высокий, а работодатели предлагают зарплату от 200 000 рублей и выше.</p>`
	if issues := CheckProgramFacts(markup, factsBook, nil); len(issues) > 0 {
		t.Fatalf("earnings in prose were checked as price: %v", issues)
	}
	wrong := `<p>Зарплата высокая. Стоимость обучения — от 9 000 ₽.</p>`
	if issues := CheckProgramFacts(wrong, factsBook, nil); len(issues) == 0 {
		t.Fatal("a wrong course price after an earnings sentence passed the check")
	}
}

// Доход помесячно без слов о зарплате (страница 32) и срок доставки документа (страница 42)
// — не цена и не объём программы.
func TestCheckProgramFactsSkipsMonthlyAmountAndTimeSpan(t *testing.T) {
	markup := `<p>Коммунальные службы предлагают от 65 000 до 120 000 рублей в месяц.</p>` +
		`<p>Скан удостоверения придёт на почту в течение 24 часов после экзамена.</p>` +
		`<p>Объём — 150 часов.</p>`
	if issues := CheckProgramFacts(markup, factsBook, nil); len(issues) > 0 {
		t.Fatalf("monthly amount or time span was checked as program fact: %v", issues)
	}
}

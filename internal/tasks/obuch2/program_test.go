package obuch2

import (
	"strings"
	"testing"
)

// Разметка модулей ровно та, что просит промпт: список без заголовков, часы в жирном зачине.
const programMarkup = `<h2 class="wp-block-heading">Детальная программа обучения</h2>` +
	`<ol class="wp-block-list">` +
	`<li><strong>Модуль 1. Нормативно-правовое регулирование — 20 часов.</strong> Гражданское и туристское законодательство РФ.</li>` +
	`<li><strong>Модуль 2. Основы экскурсоведения — 18 часов.</strong> Классификация экскурсий и структура маршрута.</li>` +
	`</ol>` +
	`<h2 class="wp-block-heading">Что вы получите по итогам курса</h2>` +
	`<ul class="wp-block-list"><li><strong>Свидетельство о профессии рабочего.</strong> Вносится в ФИС ФРДО.</li></ul>`

// Аккордеон площадки собирается из трёх полей на модуль, и каждое обязано приехать своим:
// номер модуля и часы в тему не попадают, раскрытие — в описание.
func TestParseModulesSplitsTopicHoursAndText(t *testing.T) {
	modules := ParseModules(programMarkup)

	if len(modules) != 2 {
		t.Fatalf("модулей %d вместо двух: %+v", len(modules), modules)
	}
	first := modules[0]
	if first.Topic != "Нормативно-правовое регулирование" {
		t.Fatalf("тема разобрана как %q", first.Topic)
	}
	if first.Hours != "20" {
		t.Fatalf("часы разобраны как %q", first.Hours)
	}
	if first.Text != "Гражданское и туристское законодательство РФ." {
		t.Fatalf("описание разобрано как %q", first.Text)
	}
}

// Список документов стоит под своим H2 и с виду неотличим от модуля без часов. Разбор идёт
// по разделу программы, и чужой список в него попадать не должен.
func TestParseModulesTakesOnlyProgramSection(t *testing.T) {
	for _, module := range ParseModules(programMarkup) {
		if module.Topic == "Свидетельство о профессии рабочего" {
			t.Fatalf("в модули попала строка чужого раздела: %+v", module)
		}
	}
}

// Часы сверяются с колонкой книги: то же число человек ставит в поле записи, и разойтись им
// нельзя.
func TestTotalHoursSumsModules(t *testing.T) {
	if total := TotalHours(ParseModules(programMarkup)); total != 38 {
		t.Fatalf("сумма часов %d вместо 38", total)
	}
}

// Книга без объёма программы запрещает выдумывать часы, и тогда строка остаётся одной темой.
// Модуль от этого модулем быть не перестаёт.
func TestParseModulesWithoutHours(t *testing.T) {
	markup := `<h2 class="wp-block-heading">Программа обучения</h2>` +
		`<ol class="wp-block-list">` +
		`<li><strong>Модуль 1. Охрана труда.</strong> Инструктажи и средства защиты.</li>` +
		`</ol>`

	modules := ParseModules(markup)

	if len(modules) != 1 {
		t.Fatalf("модулей %d вместо одного: %+v", len(modules), modules)
	}
	if modules[0].Hours != "" {
		t.Fatalf("часы взялись из ниоткуда: %q", modules[0].Hours)
	}
	if modules[0].Topic != "Охрана труда" {
		t.Fatalf("тема разобрана как %q", modules[0].Topic)
	}
}

// Ненайденный раздел — законный исход, а не паника: страница уже написана и оплачена, а что
// делать с пустой программой, решает публикация.
func TestParseModulesReturnsNothingWithoutSection(t *testing.T) {
	if modules := ParseModules(`<h2>Кому подойдёт</h2><p>Текст.</p>`); len(modules) != 0 {
		t.Fatalf("модули нашлись там, где раздела нет: %+v", modules)
	}
}

// Селект наугад хуже незаполненного: в записи его уже не отличить от выбранного человеком.
// Значения сняты с живых страниц площадки — по 12 на тип, единогласно.
func TestProgramPresetsMatchMeasuredPages(t *testing.T) {
	rabprof, known := ProgramPresetOf("rabprof")
	if !known || rabprof.Document != "svidetelstvo" || rabprof.Attestation != "exam" {
		t.Fatalf("rabprof разъехался с замером: %+v", rabprof)
	}
	povyshenie, known := ProgramPresetOf("povyshenie")
	if !known || povyshenie.Document != "udostoverenie" || povyshenie.Attestation != "test" {
		t.Fatalf("povyshenie разъехался с замером: %+v", povyshenie)
	}
	// Заголовок блока у повышения квалификации свой, и это не мелочь: подпись аккордеона
	// видит читатель, а «Программа обучения» над программой ПК — чужая подпись.
	if rabprof.Heading == povyshenie.Heading {
		t.Fatalf("заголовок блока одинаков у обоих типов: %q", rabprof.Heading)
	}
	if povyshenie.Heading != "Программа повышения квалификации" {
		t.Fatalf("заголовок ПК = %q", povyshenie.Heading)
	}
	if _, known := ProgramPresetOf("perepodgotovka"); known {
		t.Fatal("незамеренный тип записи объявлен известным")
	}
}

func TestStripModuleHoursLeavesTopicAndDescription(t *testing.T) {
	markup := `<h2>Программа обучения</h2><ol class="wp-block-list"><li><strong>Модуль 1. Устройство крана — 16 часов.</strong> Механизмы и тормоза.</li>` +
		`<li><strong style="display:block">Модуль 2. Итоговая аттестация — 4 часа.</strong><span>Экзамен.</span></li></ol>` +
		`<h2>Доход</h2><p><strong>Итог — 150 часов.</strong></p>`
	got := StripModuleHours(markup)
	for _, want := range []string{"<strong>Модуль 1. Устройство крана.</strong> Механизмы и тормоза.", `<strong style="display:block">Модуль 2. Итоговая аттестация.</strong>`, "<strong>Итог — 150 часов.</strong>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("StripModuleHours() lost %q in:\n%s", want, got)
		}
	}
	if modules := ParseModules(markup); len(modules) != 2 || modules[0].Hours != "16" {
		t.Fatalf("ParseModules on the stored markup = %+v, hours must still be there", modules)
	}
}

// «Кому подойдёт эта программа» стоит первым разделом, и её список портретов разбором
// модулей быть не должен (страница 21).
func TestParseModulesSkipsAudienceSectionNamedProgram(t *testing.T) {
	markup := `<h2>Кому подойдёт эта программа</h2><ul><li><strong>Новичкам.</strong> Без опыта.</li></ul>` +
		`<h2>Детальная программа обучения</h2><ol><li><strong>Модуль 1. Материаловедение — 6 часов.</strong> Свойства металлов.</li></ol>` +
		`<h2>Частые вопросы</h2>`
	modules := ParseModules(markup)
	if len(modules) != 1 || modules[0].Topic != "Материаловедение" || modules[0].Hours != "6" {
		t.Fatalf("modules parsed from the wrong section: %+v", modules)
	}
	if stripped := StripModuleHours(markup); !strings.Contains(stripped, "Материаловедение.</strong>") {
		t.Fatalf("hours were not stripped from the module section: %s", stripped)
	}
}

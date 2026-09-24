package generation

import (
	"path/filepath"
	"strings"
	"testing"
)

// projectRoot — корень дерева относительно каталога пакета.
const projectRoot = "../../.."

// liveBlockTemplatesDir — боевой каталог шаблонов. Путь написан здесь, а не взят из
// internal/tasks: движок про задачи не знает, и тест движка не имеет права заводить эту
// зависимость ради одной строки. Переехавший каталог тест уронит на чтении — именно этого
// от него и ждут.
const liveBlockTemplatesDir = "tasks/common/templates/blocks"

// liveBlockTemplates — боевые шаблоны блоков. Тесты идут по ним, а не по выдуманным:
// разойдись имена полей структуры и плейсхолдеров файла — шаблон подставит пустую строку,
// а не откажет, и поймать это можно только здесь.
func liveBlockTemplates(t *testing.T) *BlockTemplates {
	t.Helper()
	blocks, err := ReadBlockTemplates(filepath.Join(projectRoot, filepath.FromSlash(liveBlockTemplatesDir)))
	if err != nil {
		t.Fatalf("шаблоны блоков не читаются: %v", err)
	}
	return blocks
}

// Помеченная цитата становится врезкой: ярлык отдельной строкой, текст — абзацем, полоса
// слева в оранжевом площадки.
func TestDecorateTurnsMarkedQuoteIntoNote(t *testing.T) {
	markup := `<blockquote class="wp-block-quote sp-note">` +
		`<p class="wp-block-paragraph"><strong>Из практики.</strong> На аттестации валятся на санитарии.</p>` +
		`</blockquote>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	for _, want := range []string{"border-left:3px solid #ff7500", "Из практики", "валятся на санитарии"} {
		if !strings.Contains(got, want) {
			t.Fatalf("во врезке нет %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<blockquote") {
		t.Fatalf("цитата осталась цитатой:\n%s", got)
	}
}

// Ряд плашек: значение отдельно от подписи, карточек столько же, сколько строк списка.
func TestDecorateTurnsMarkedListIntoStats(t *testing.T) {
	markup := `<ul class="wp-block-list sp-stats">` +
		`<li><strong>3–4 мес.</strong> — срок обучения с нуля.</li>` +
		`<li><strong>2–6</strong> — разряды повара по ЕТКС.</li>` +
		`</ul>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if count := strings.Count(got, "border-top:3px solid #ff7500"); count != 2 {
		t.Fatalf("плашек %d вместо двух:\n%s", count, got)
	}
	for _, want := range []string{"3–4 мес.", "срок обучения с нуля", "разряды повара по ЕТКС"} {
		if !strings.Contains(got, want) {
			t.Fatalf("в плашках нет %q:\n%s", want, got)
		}
	}
}

// Шаги нумерует код по порядку строк: модель нумерует не всегда, а дыра в нумерации
// читается как потерянный шаг.
func TestDecorateNumbersStepsByOrder(t *testing.T) {
	markup := `<ol class="wp-block-list sp-steps">` +
		`<li><strong>Шаг 1.</strong> Медкнижка.</li>` +
		`<li><strong>Обучение.</strong> Программа от 320 часов.</li>` +
		`</ol>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if !strings.Contains(got, ">1</div>") || !strings.Contains(got, ">2</div>") {
		t.Fatalf("шаги не пронумерованы:\n%s", got)
	}
	if strings.Contains(got, "Шаг 1.") {
		t.Fatalf("служебное «Шаг 1.» осталось в тексте шага:\n%s", got)
	}
	if !strings.Contains(got, "<strong>Обучение.</strong>") {
		t.Fatalf("жирный зачин шага потерян:\n%s", got)
	}
}

// Обычный список не трогается: у него нет метки, и перекрасить его хуже, чем не оформить
// плашки.
func TestDecorateLeavesPlainListAlone(t *testing.T) {
	markup := `<ul class="wp-block-list"><li><strong>Плюс.</strong> Стабильный спрос.</li></ul>`

	if got := DecorateBlocks(markup, liveBlockTemplates(t)); got != markup {
		t.Fatalf("обычный список переписан:\n%s", got)
	}
}

// Таблице дописывается оформление, но её содержимое остаётся моделиным до знака.
func TestDecorateStylesTableAndKeepsCells(t *testing.T) {
	markup := `<figure class="wp-block-table"><table class="has-fixed-layout"><tbody>` +
		`<tr><td><strong>Разряд</strong></td><td><strong>Что доверяют</strong></td></tr>` +
		`<tr><td><strong>2–3</strong></td><td>Заготовки и нарезка</td></tr>` +
		`</tbody></table></figure>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	for _, want := range []string{"overflow-x:auto", "border-bottom:2px solid #ff7500", "Заготовки и нарезка", "<strong>Разряд</strong>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("в таблице нет %q:\n%s", want, got)
		}
	}
}

// Таблица плюсов и минусов помечена sp-proscons, и первая колонка в ней не жирная: обе
// ячейки строки — половины разбора, а не заголовок и значение. Без этого левая колонка
// уходит в блог целиком жирной, а правая обычной, и симметричный разбор читается перекосом.
func TestDecorateKeepsProsConsColumnsEqual(t *testing.T) {
	row := `<tr><td>1. <strong>Постоянный спрос.</strong> Трубы текут всегда.</td>` +
		`<td>1. <strong>Контакт с инфекциями.</strong> Нужны прививки.</td></tr>`
	markup := `<figure class="wp-block-table sp-proscons"><table class="has-fixed-layout"><tbody>` +
		`<tr><td><strong>Преимущества</strong></td><td><strong>Недостатки</strong></td></tr>` +
		row + `</tbody></table></figure>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if !strings.Contains(got, "overflow-x:auto") {
		t.Fatalf("таблица не оформлена:\n%s", got)
	}
	body := strings.SplitN(got, "border-bottom:2px solid #ff7500", 2)[1]
	if strings.Contains(body, firstCellStyle) {
		t.Fatalf("первая колонка плюсов и минусов осталась жирной:\n%s", got)
	}
	if !strings.Contains(got, "<strong>Постоянный спрос.</strong>") {
		t.Fatalf("ярлык-тезис потерян:\n%s", got)
	}
	// Метка обязана дожить до готовой разметки: оформление собирает <figure> заново, и без
	// переноса по собранному файлу не отличить помеченную таблицу от непомеченной.
	if !strings.Contains(got, `class="wp-block-table sp-proscons"`) {
		t.Fatalf("метка не дожила до готовой разметки:\n%s", got)
	}
}

// Метка снимает жирность только у помеченной таблицы: обычная её сохраняет.
func TestDecorateKeepsFirstCellBoldWithoutMarker(t *testing.T) {
	markup := `<figure class="wp-block-table"><table class="has-fixed-layout"><tbody>` +
		`<tr><td><strong>Разряд</strong></td><td><strong>Что доверяют</strong></td></tr>` +
		`<tr><td><strong>2–3</strong></td><td>Заготовки и нарезка</td></tr>` +
		`</tbody></table></figure>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if !strings.Contains(got, firstCellStyle) {
		t.Fatalf("первая ячейка строки перестала быть жирной:\n%s", got)
	}
}

// Вопрос в FAQ заголовком не является: заголовок у блока один — его H2. Вопросы приходят
// строками списка с жирным зачином и такими же остаются, меняется только вид строки.
func TestDecorateFAQTurnsListIntoCards(t *testing.T) {
	markup := `<h2 class="wp-block-heading">Частые вопросы</h2>` +
		`<ol class="wp-block-list">` +
		`<li><strong>Берут ли без опыта?</strong><br>Берут, на 2–3 разряд.</li>` +
		`<li><strong>Нужен ли очный экзамен?</strong> Да, квалификационная проба очная.</li>` +
		`</ol>` +
		`<h2 class="wp-block-heading">Запишитесь на обучение</h2>` +
		`<p class="wp-block-paragraph">Финальный абзац.</p>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if strings.Contains(got, "<h3") {
		t.Fatalf("вопрос стал заголовком:\n%s", got)
	}
	for _, want := range []string{"Берут ли без опыта?", "Берут, на 2–3 разряд.",
		"Нужен ли очный экзамен?", "Да, квалификационная проба очная."} {
		if !strings.Contains(got, want) {
			t.Fatalf("текст %q потерян при оформлении:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "border-left:2px solid #ffd9b8") {
		t.Fatalf("строки вопросов не оформлены:\n%s", got)
	}
	if strings.Contains(strings.SplitN(got, "Запишитесь на обучение", 2)[1], "border-left:2px solid #ffd9b8") {
		t.Fatalf("оформление FAQ уехало за пределы раздела:\n%s", got)
	}
}

// Прежняя форма — «H3 вопрос, абзац ответ» — переводится в тот же список: страницы,
// написанные до смены правила, обязаны оформляться так же, как новые.
func TestDecorateFAQConvertsOldHeadingForm(t *testing.T) {
	markup := `<h2 class="wp-block-heading">Часто задаваемые вопросы (FAQ)</h2>` +
		`<h3 class="wp-block-heading">Берут ли без опыта</h3>` +
		`<p class="wp-block-paragraph">Берут, на 2–3 разряд.</p>` +
		`<h2 class="wp-block-heading">Запишитесь на обучение</h2>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if strings.Contains(got, "<h3") {
		t.Fatalf("вопрос остался заголовком:\n%s", got)
	}
	if !strings.Contains(got, "Берут ли без опыта") || !strings.Contains(got, "Берут, на 2–3 разряд.") {
		t.Fatalf("пара потеряна при переводе:\n%s", got)
	}
}

// Раздел заменяется целиком, поэтому текст между вопросами обязан его удержать: абзац там
// написан человеком, и потерять его ради оформления нельзя.
func TestDecorateFAQKeepsSectionWithExtraText(t *testing.T) {
	markup := `<h2 class="wp-block-heading">Часто задаваемые вопросы (FAQ)</h2>` +
		`<p class="wp-block-paragraph">Собрали то, что спрашивают чаще всего.</p>` +
		`<h3 class="wp-block-heading">Берут ли без опыта</h3>` +
		`<p class="wp-block-paragraph">Берут, на 2–3 разряд.</p>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if !strings.Contains(got, "Собрали то, что спрашивают чаще всего.") {
		t.Fatalf("вводный абзац раздела потерян:\n%s", got)
	}
}

// Строка источника оформляется сноской, а не остаётся абзацем вровень с текстом.
func TestDecorateMarksSourceLine(t *testing.T) {
	markup := `<p class="wp-block-paragraph">Источник: hh.ru, сентябрь 2026</p>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if !strings.Contains(got, "background:#ff7500") || !strings.Contains(got, "hh.ru") {
		t.Fatalf("источник не оформлен:\n%s", got)
	}
}

// Без шаблонов разметка возвращается нетронутой: оформление блоков — украшение, и его
// отсутствие не имеет права менять текст статьи.
func TestDecorateWithoutTemplatesKeepsMarkup(t *testing.T) {
	markup := `<ul class="wp-block-list sp-stats"><li><strong>2–6</strong> — разряды.</li></ul>`

	if got := DecorateBlocks(markup, nil); got != markup {
		t.Fatalf("разметка изменена без шаблонов:\n%s", got)
	}
}

// Одноколоночная таблица шапки не имеет: её первая строка — такое же значение, как прочие.
// Так свёрстана плашка параметров страницы услуги, и покрашенная шапкой строка «Срок: …»
// объявила бы заголовком обычную графу.
func TestDecorateLeavesSingleColumnTableWithoutHead(t *testing.T) {
	markup := `<figure class="wp-block-table"><table class="has-fixed-layout"><tbody>` +
		`<tr><td><strong>Срок:</strong> 150 часов, от 2 недель.</td></tr>` +
		`<tr><td><strong>Цена:</strong> от 5 000 ₽.</td></tr>` +
		`</tbody></table></figure>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if !strings.Contains(got, "overflow-x:auto") {
		t.Fatalf("плашка не оформлена:\n%s", got)
	}
	if strings.Contains(got, headRowStyle) || strings.Contains(got, headCellStyle) {
		t.Fatalf("у одноколоночной плашки появилась шапка:\n%s", got)
	}
	for _, want := range []string{"<strong>Срок:</strong> 150 часов, от 2 недель.", "от 5 000 ₽."} {
		if !strings.Contains(got, want) {
			t.Fatalf("в плашке нет %q:\n%s", want, got)
		}
	}
}

// Многоколоночная таблица шапку сохраняет: правило про одну колонку не имеет права задеть
// таблицы статей блога, где нулевая строка — настоящие заголовки колонок.
func TestDecorateKeepsHeadOnMultiColumnTable(t *testing.T) {
	markup := `<figure class="wp-block-table"><table class="has-fixed-layout"><thead>` +
		`<tr><td><strong>Опыт</strong></td><td><strong>Средний доход (руб.)</strong></td><td><strong>Где работает</strong></td></tr>` +
		`</thead><tbody>` +
		`<tr><td>До года</td><td>45 000 — 60 000</td><td>Гальванический участок</td></tr>` +
		`</tbody></table></figure>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	if !strings.Contains(got, headRowStyle) {
		t.Fatalf("шапка таблицы заработка не покрашена:\n%s", got)
	}
	if !strings.Contains(got, "Гальванический участок") {
		t.Fatalf("третья колонка потеряна:\n%s", got)
	}
}

// Заголовок блока вопросов узнаётся в обеих формах площадки: «Часто задаваемые вопросы» у
// статей блога и «Частые вопросы» у страниц услуг. Форму диктует скелет задачи, и узкое
// правило оставило бы половину страниц без оформленного блока — молча.
func TestDecorateFindsFAQByBothHeadings(t *testing.T) {
	for _, heading := range []string{
		"Часто задаваемые вопросы (FAQ)",
		"Частые вопросы об обучении гальваника",
	} {
		markup := `<h2 class="wp-block-heading">` + heading + `</h2>` +
			`<h3 class="wp-block-heading">Нужен ли разряд?</h3>` +
			`<p class="wp-block-paragraph">Да, без него к работам не допускают.</p>`

		got := DecorateBlocks(markup, liveBlockTemplates(t))

		if !strings.Contains(got, "border-left:2px solid #ffd9b8") {
			t.Fatalf("блок вопросов не оформлен при заголовке %q:\n%s", heading, got)
		}
		if !strings.Contains(got, "Нужен ли разряд?") {
			t.Fatalf("вопрос потерян при заголовке %q:\n%s", heading, got)
		}
	}
}

// Подпись отзыва приходит двумя формами: промпт просит её в конце после тире, а модель
// устойчиво ставит имя первым предложением. Узнаваться обязаны обе — иначе строка под чертой
// уходит в блог пустой, а имя остаётся торчать в середине текста.
func TestDecorateReviewsFindsAuthorInBothForms(t *testing.T) {
	markup := `<ul class="wp-block-list sp-reviews">` +
		`<li>Михаил, 29 лет, г. Москва. Работал водителем, ушёл на кран.</li>` +
		`<li>Учился вечерами, сдал с первого раза. — Анна К., 41 год, г. Екатеринбург</li>` +
		`</ul>`

	got := DecorateBlocks(markup, liveBlockTemplates(t))

	for _, want := range []string{"Михаил, 29 лет, г. Москва", "Анна К., 41 год, г. Екатеринбург"} {
		if !strings.Contains(got, want) {
			t.Fatalf("подпись %q потеряна:\n%s", want, got)
		}
	}
	// Подпись вынесена из текста, а не продублирована в нём.
	if strings.Contains(got, "Михаил, 29 лет, г. Москва. Работал") {
		t.Fatalf("подпись осталась внутри текста отзыва:\n%s", got)
	}
	if strings.Count(got, "font-style:italic") != 2 {
		t.Fatalf("отзывов оформлено не два:\n%s", got)
	}
}

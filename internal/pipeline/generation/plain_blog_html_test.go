package generation

import (
	"strings"
	"testing"
)

// На площадке без инлайновых стилей их не должно остаться ни одного — это и есть главное
// требование её вёрстки, измеренное по 37 опубликованным статьям.
func TestCleanPlainBlogMarkupDropsStylesAndDivs(t *testing.T) {
	markup := `<div class="ds-scroll-area _1210dd7"><table style="width:100%;">` +
		`<thead><tr><td><strong>Разряд</strong></td><td><strong>Срок</strong></td></tr></thead>` +
		`<tbody><tr><td><strong>3-й</strong></td><td>2 мес</td></tr></tbody></table></div>` +
		`<p class="ds-markdown-paragraph"><span class="">Абзац.</span></p>`

	cleaned := CleanPlainBlogMarkup(markup)

	if strings.Contains(cleaned, "style=") {
		t.Fatalf("в разметке остался инлайновый стиль:\n%s", cleaned)
	}
	if strings.Contains(cleaned, "<div") || strings.Contains(cleaned, "</div>") {
		t.Fatalf("в разметке остался div:\n%s", cleaned)
	}
	if strings.Contains(cleaned, "ds-") {
		t.Fatalf("в разметке остался класс веб-интерфейса:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, `<figure class="wp-block-table">`) {
		t.Fatalf("таблица не завёрнута в figure:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, `<table class="has-fixed-layout">`) {
		t.Fatalf("таблице не дописан класс раскладки:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, `<p class="wp-block-paragraph">Абзац.</p>`) {
		t.Fatalf("абзац не получил класс темы:\n%s", cleaned)
	}
}

// Врезка на этой площадке — цитата. Пара тегов обязана закрыться: открывающий div без своей
// пары оставил бы в записи «</div>» посреди текста.
func TestCleanPlainBlogMarkupTurnsNoticeIntoQuote(t *testing.T) {
	markup := `<div class="ds-notice"><p><span class="ds-notice__label">Из практики.</span> Текст.</p></div>`

	cleaned := CleanPlainBlogMarkup(markup)

	if !strings.Contains(cleaned, `<blockquote class="wp-block-quote`) {
		t.Fatalf("врезка не стала цитатой:\n%s", cleaned)
	}
	if strings.Count(cleaned, "<blockquote") != strings.Count(cleaned, "</blockquote>") {
		t.Fatalf("цитата не закрыта:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "<strong>Из практики.</strong>") {
		t.Fatalf("подпись врезки потеряна:\n%s", cleaned)
	}
}

// Таблица, которую модель уже завернула правильно, второй обёртки не получает.
func TestCleanPlainBlogMarkupKeepsSingleFigure(t *testing.T) {
	markup := `<figure class="wp-block-table"><table class="has-fixed-layout"><tbody><tr><td>a</td><td>b</td></tr></tbody></table></figure>`

	cleaned := CleanPlainBlogMarkup(markup)

	if got := strings.Count(cleaned, "<figure"); got != 1 {
		t.Fatalf("обёрток таблицы %d, ожидалась одна:\n%s", got, cleaned)
	}
}

// Ряд плашек собирается списком, а не div'ами с инлайновыми стилями.
func TestRestoreStatsListBuildsList(t *testing.T) {
	page := "Лид статьи.\nПЛАШКИ: 2–4 мес. | срок подготовки ;; 3 разряд | стартовая ступень\nH2 - Раздел"
	markup := `<p class="wp-block-paragraph">Лид статьи.</p><h2 class="wp-block-heading">Раздел</h2>`

	restored := RestoreStatsList(page, markup)

	if !strings.Contains(restored, `<ul class="wp-block-list">`) {
		t.Fatalf("плашки не стали списком:\n%s", restored)
	}
	if !strings.Contains(restored, "<strong>2–4 мес.</strong> — срок подготовки.") {
		t.Fatalf("первая плашка потеряна:\n%s", restored)
	}
	if strings.Contains(restored, "style=") {
		t.Fatalf("список плашек получил инлайновый стиль:\n%s", restored)
	}
	// Блок встаёт после вводного абзаца, до первого заголовка.
	if strings.Index(restored, "<ul") > strings.Index(restored, "<h2") {
		t.Fatalf("плашки встали после заголовка:\n%s", restored)
	}
}

// Уже свёрстанный моделью список плашек не дублируется.
func TestRestoreStatsListSkipsRenderedBlock(t *testing.T) {
	page := "Лид.\nПЛАШКИ: 2–4 мес. | срок подготовки\nH2 - Раздел"
	markup := `<p class="wp-block-paragraph">Лид.</p><ul class="wp-block-list"><li><strong>2–4 мес.</strong> — срок подготовки.</li></ul>`

	if restored := RestoreStatsList(page, markup); restored != markup {
		t.Fatalf("плашки продублированы:\n%s", restored)
	}
}

// Строки плашек в тексте нет — код ничего не выдумывает.
func TestRestoreStatsListWithoutMarkerKeepsMarkup(t *testing.T) {
	markup := `<p class="wp-block-paragraph">Лид.</p>`

	if restored := RestoreStatsList("Лид статьи без плашек.", markup); restored != markup {
		t.Fatalf("разметка изменена без строки плашек:\n%s", restored)
	}
}

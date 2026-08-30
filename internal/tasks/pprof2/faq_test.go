package pprof2

import (
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/result"
)

// Текст страницы приходит в том виде, в каком его пишет модель: заголовки строками «H2 - …».
const pageWithFAQ = `H1 - Обучение на стропальщика

Вводный абзац о профессии.

H2 - Кому подходит обучение

Тем, кто работает с грузоподъёмными механизмами.

H2 - Частые вопросы об обучении стропальщиков

Ниже собраны вопросы, которые задают чаще всего.

H3 - Какие требования к базовому образованию?

Достаточно основного общего образования и возраста от 18 лет.

H3 - Какой документ выдают после обучения?

Удостоверение стропальщика с присвоением разряда.
Сведения вносятся в реестр.

H2 - Оставить заявку на обучение

Заполните форму, и специалист свяжется с вами.`

// Разбор обязан отдавать ровно тот формат, который читает сборка result.md и публикация:
// иначе FAQ молча не доедет ни до одного из них.
func TestExtractFAQReturnsParsablePairs(t *testing.T) {
	faq := ExtractFAQ(pageWithFAQ)
	items, err := result.ParseFAQItems(faq)
	if err != nil {
		t.Fatalf("FAQ не разбирается обратно: %v\n%s", err, faq)
	}
	if len(items) != 2 {
		t.Fatalf("вопросов %d, ожидалось 2: %+v", len(items), items)
	}
	if items[0].Question != "Какие требования к базовому образованию?" {
		t.Fatalf("первый вопрос: %q", items[0].Question)
	}
	if !strings.Contains(items[1].Answer, "Сведения вносятся в реестр.") {
		t.Fatalf("ответ потерял продолжение: %q", items[1].Answer)
	}
}

// Вводный абзац блока относится к странице, а не к паре «вопрос — ответ».
func TestExtractFAQSkipsBlockIntro(t *testing.T) {
	faq := ExtractFAQ(pageWithFAQ)
	if strings.Contains(faq, "Ниже собраны вопросы") {
		t.Fatalf("вводный абзац попал в FAQ:\n%s", faq)
	}
}

// Блок кончается следующим H2: призыв оставить заявку — уже не ответ на вопрос.
func TestExtractFAQStopsAtNextSection(t *testing.T) {
	faq := ExtractFAQ(pageWithFAQ)
	if strings.Contains(faq, "Заполните форму") {
		t.Fatalf("текст следующего раздела попал в FAQ:\n%s", faq)
	}
}

// Страница без блока частых вопросов — законное состояние: генерацию оно не роняет, а
// публикацию остановит проверка публикации.
func TestExtractFAQReturnsEmptyWithoutBlock(t *testing.T) {
	if faq := ExtractFAQ("H1 - Обучение\n\nH2 - Программа\n\nМодули и практика."); faq != "" {
		t.Fatalf("FAQ взялся ниоткуда: %q", faq)
	}
}

// «Вопросы» в названии подраздела программы блоком частых вопросов не являются.
func TestExtractFAQIgnoresDeepHeadings(t *testing.T) {
	page := "H2 - Программа обучения\n\nH3 - Частые вопросы по модулю\n\nОтвет внутри модуля."
	if faq := ExtractFAQ(page); faq != "" {
		t.Fatalf("подраздел принят за блок частых вопросов: %q", faq)
	}
}

// Модель иногда оформляет вопросы не заголовком, а жирной строкой — блок от этого не пропадает.
func TestExtractFAQReadsPlainQuestions(t *testing.T) {
	page := "H2 - Часто задаваемые вопросы\n\n**Сколько длится обучение?**\n\nОт двух недель.\n\n" +
		"**Нужен ли опыт работы?**\n\nНет, программа рассчитана на новичков."
	items, err := result.ParseFAQItems(ExtractFAQ(page))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Question != "Сколько длится обучение?" {
		t.Fatalf("вопросы разобраны неверно: %+v", items)
	}
}

// Ревью снимает с вопросов заголовки и приклеивает ответ к той же строке. Формат сорван, но
// блок вопросов уже написан и оплачен — терять его из-за разметки нельзя.
func TestExtractFAQReadsQuestionGluedToAnswer(t *testing.T) {
	page := "H2 - Частые вопросы\n\n**Какой документ выдаётся?** Диплом установленного образца.\n\n" +
		"**Как проходит практика?** На площадке учебного центра."
	items, err := result.ParseFAQItems(ExtractFAQ(page))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("склеенные пары разобраны неверно: %+v", items)
	}
	if items[0].Question != "Какой документ выдаётся?" || items[0].Answer != "Диплом установленного образца." {
		t.Fatalf("вопрос не отделён от ответа: %+v", items[0])
	}
}

// Вопрос без ответа не сохраняется: разбор на чтении отвергает такую пару и унёс бы с собой
// весь блок целиком.
func TestExtractFAQDropsQuestionWithoutAnswer(t *testing.T) {
	page := "H2 - Частые вопросы\n\nH3 - Вопрос без ответа?\n\nH3 - Какой документ выдают?\n\nУдостоверение."
	items, err := result.ParseFAQItems(ExtractFAQ(page))
	if err != nil {
		t.Fatalf("FAQ не разбирается: %v", err)
	}
	if len(items) != 1 || items[0].Question != "Какой документ выдают?" {
		t.Fatalf("оборванная пара не отброшена: %+v", items)
	}
}

// Промпт запрещает Markdown-заголовки, но разбор читает то, что модель прислала на самом
// деле: сорванный формат не должен стоить статье всего блока вопросов.
func TestExtractFAQReadsMarkdownHeadings(t *testing.T) {
	page := "## FAQ\n\n### Как проходит практика?\n\nНа площадке учебного центра."
	items, err := result.ParseFAQItems(ExtractFAQ(page))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Question != "Как проходит практика?" {
		t.Fatalf("вопросы разобраны неверно: %+v", items)
	}
}

// Ревью иногда снимает маркеры заголовков со всей страницы: блок вопросов остаётся на месте,
// но «H2 -» перед ним нет. Так приехала статья 19 — вопросы в тексте есть, а публикация
// отказывала по пустому FAQ.
func TestExtractFAQReadsBlockWithoutHeadingMarkers(t *testing.T) {
	page := "H1: Медицинская статистика — обучение\n\n" +
		"Вводный абзац о профессии.\n\n" +
		"Кому подходит обучение\n\n" +
		"Специалистам с медицинским образованием.\n\n" +
		"Частые вопросы о переподготовке на медицинского статистика\n\n" +
		"Какое базовое образование нужно?\n\n" +
		"Среднее профессиональное или высшее медицинское.\n\n" +
		"Какой документ выдается после обучения?\n\n" +
		"Диплом о профессиональной переподготовке установленного образца.\n\n" +
		"Станьте медицинским статистиком — оставьте заявку на обучение\n\n" +
		"Вы проходите переподготовку дистанционно, в объёме от 250 часов.\n"
	items, err := result.ParseFAQItems(ExtractFAQ(page))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("вопросов %d, ожидалось 2: %+v", len(items), items)
	}
	if items[1].Answer != "Диплом о профессиональной переподготовке установленного образца." {
		t.Fatalf("ответ второй пары: %q", items[1].Answer)
	}
	if strings.Contains(ExtractFAQ(page), "оставьте заявку") {
		t.Fatalf("следующий раздел страницы попал в FAQ:\n%s", ExtractFAQ(page))
	}
}

// Без маркеров заголовок блока отличается от абзаца только смыслом, поэтому упоминание
// частых вопросов внутри текста блоком не считается: иначе в FAQ уехала бы половина страницы.
func TestExtractFAQIgnoresMentionOfQuestionsInText(t *testing.T) {
	page := "H1: Обучение\n\n" +
		"На вебинарах разбираются частые вопросы слушателей о практике и документах.\n\n" +
		"Сколько длится обучение?\n\nОт двух недель."
	if faq := ExtractFAQ(page); faq != "" {
		t.Fatalf("абзац с упоминанием принят за блок:\n%s", faq)
	}
}

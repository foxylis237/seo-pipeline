package pprof1

import (
	"context"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Плейсхолдеры шаблона и поля ctaCard обязаны совпадать: text/template на лишнее поле
// откажет, а на недостающее — молча оставит в карточке дыру.
func TestCTACardTemplateMatchesSlots(t *testing.T) {
	raw, err := os.ReadFile(testCTACardPath)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, match := range regexp.MustCompile(`\{\{\.(\w+)\}\}`).FindAllStringSubmatch(string(raw), -1) {
		used[match[1]] = true
	}
	var fields, placeholders []string
	for i := 0; i < reflect.TypeOf(ctaCard{}).NumField(); i++ {
		fields = append(fields, reflect.TypeOf(ctaCard{}).Field(i).Name)
	}
	for name := range used {
		placeholders = append(placeholders, name)
	}
	sort.Strings(fields)
	sort.Strings(placeholders)
	if strings.Join(fields, ",") != strings.Join(placeholders, ",") {
		t.Fatalf("слоты шаблона %v не совпадают с полями карточки %v", placeholders, fields)
	}
	for _, marker := range []string{ctaCardMarker, ctaButtonMarker} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("в шаблоне нет класса %s", marker)
		}
	}
}

// Ответ модели с нумерацией, кавычками и лишней строкой разбирается; кавычки и разметка
// экранируются, а адрес кнопки берётся из книги.
func TestHTMLStageAppendsCTACard(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	chats.queue = map[string][]string{StageHTML: {
		htmlWithLink,
		"Вот строки:\n1. бейдж ;; Профессиональное обучение\n2. заголовок ;; «Станьте логопедом <за 3 месяца>»\n" +
			"описание ;; Учим диагностике и коррекции речи.\nквалификация ;; Логопед\nдокумент ;; Диплом в госреестре\nформат ;; Дистанционно",
	}}
	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	if err := flow.RunHTML(ctx, "7"); err != nil {
		t.Fatalf("html: %v", err)
	}
	saved, err := writer.Read(repository.htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`class="sp-cta-card"`, `href="https://example.test/course"`, "Профессиональное обучение",
		"Станьте логопедом &lt;за 3 месяца&gt;", "Диплом в госреестре", "Записаться на обучение",
	} {
		if !strings.Contains(saved, want) {
			t.Fatalf("в карточке нет %q: %s", want, saved)
		}
	}
	if strings.Index(saved, "sp-cta-card") < strings.Index(saved, "логопеда</a>") {
		t.Fatal("карточка стоит не в конце статьи")
	}
}

// Модель нарисовала свой призыв — второй карточки код не ставит.
func TestAppendCTACardSkipsExistingButton(t *testing.T) {
	markup := `<p>текст</p><a class="sp-cta-button" href="#">Записаться</a>`
	if _, added := appendCTACard(markup, "<div>карточка</div>"); added {
		t.Fatal("вторая карточка дописана поверх нарисованной моделью")
	}
}

// Пустой ответ модели карточку не отменяет: она собирается на умолчаниях.
func TestBuildCTACardFallsBackToDefaults(t *testing.T) {
	card, missing := buildCTACard(parseCTASlots("не понял задачу"), "https://example.test/course")
	if len(missing) != 6 {
		t.Fatalf("умолчаниями заменено %v, ожидалось шесть слотов", missing)
	}
	if card.Heading == "" || card.ButtonURL != "https://example.test/course" {
		t.Fatalf("карточка на умолчаниях собрана неверно: %+v", card)
	}
}

// Без адреса кнопки статья падает до первого сообщения модели.
func TestStructureRequiresCourseURL(t *testing.T) {
	flow, chats, repository, _, _ := newFlowFixture(t)
	repository.input.CourseURL = " "
	if err := flow.RunStructure(context.Background(), "7"); err == nil {
		t.Fatal("статья без course_url ушла в генерацию")
	}
	if len(chats.chats) != 0 {
		t.Fatalf("чат открыт до проверки адреса: %v", chats.chats)
	}
}

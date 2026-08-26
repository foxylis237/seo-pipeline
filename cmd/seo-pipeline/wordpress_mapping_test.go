package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

const (
	testCoursePath      = "2-laboratornoe-delo/article.html"
	testCourseResult    = "2-laboratornoe-delo/result.md"
	testCourseHTML      = `<p class="ds-markdown-paragraph">Текст страницы услуги</p>`
	testCoursePriceText = "Цена курсов: от 5 000 ₽\nКод программы 1011\nГрафик — гибкий\nДистанционное обучение"
	// Лист страницы услуги: alt приходит из фокусного ключа, слаг картинки — свой раздел,
	// блок стоимости задан шаблоном и в книге его нет.
	testCourseResultMD = "## Заголовок\n\n```text\nЛабораторное дело\n```\n\n" +
		"## Атрибут \"alt\" у главной картинки\n\n```text\nЛабораторное дело\n```\n\n" +
		"## Синий блок со стоимостью\n\n```text\n" + testCoursePriceText + "\n```\n\n" +
		"## HTML\n\n```text\n" + testCoursePath + "\n```\n"
)

func readyCoursePublicationInput() article.PublicationInput {
	return article.PublicationInput{
		Article: article.Article{
			ID: 2, ExternalID: "2", Title: "Лабораторное дело - дистанционное обучение",
			Status: "completed", Slug: "laboratornoe-delo",
		},
		Publication:     article.Publication{Status: article.WordPressNotPublished},
		Category:        "Обучение медперсонала",
		Keyword:         "Лабораторное дело",
		MetaDescription: "Лабораторное дело: обучение с применением ДОТ.",
		Header:          "Курсы [blue]лаборанта[/blue]",
		SEOTitle:        "Лабораторное дело — обучение с ДОТ и записью в ФИС ФРДО",
		Teachers:        "Соколовская Елена Романовна",
		Profession:      "Лаборант",
		FAQ:             "Вопрос: Какое образование нужно?\nОтвет: Среднее профессиональное медицинское.",
		HTMLPath:        testCoursePath,
	}
}

// newCoursePublishDeps собирает публикацию страницы услуги: раскладка своя, всё остальное —
// тот же сценарий, что и у статьи блога.
func newCoursePublishDeps() (wordPressPublishDeps, *fakeWPRepository, *fakeWPClient, *bytes.Buffer) {
	repository := &fakeWPRepository{input: readyCoursePublicationInput()}
	client := &fakeWPClient{categoryID: 1106951, teacherID: 21785, attachmentID: 30100}
	out := &bytes.Buffer{}
	deps := wordPressPublishDeps{
		client:      client,
		mapping:     courseWordPressMapping{},
		repository:  repository,
		writer:      &fakeWPWriter{files: map[string]string{testCoursePath: testCourseHTML, testCourseResult: testCourseResultMD}},
		images:      newWPImages(),
		resultBuild: &fakeWPResultBuilder{},
		logger:      slog.New(slog.DiscardHandler),
		out:         out,
		assumeYes:   true,
		// Признаки профиля pprof_2: стадии info нет, но FAQ есть — он вынут из текста страницы.
		withoutArticleMetadata: true,
		metadataFAQOnly:        true,
	}
	return deps, repository, client, out
}

// Страница услуги ложится в свой тип записи и свою таксономию, а меток не получает вовсе.
func TestCourseMappingSendsOwnTypeTaxonomyAndNoTags(t *testing.T) {
	deps, _, client, _ := newCoursePublishDeps()
	payload, plan, err := buildWordPressPayload(context.Background(), deps, "2", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	// Тип записи и таксономия сняты с живой страницы курса 22065, а не выведены из умолчаний.
	if payload.PostType != "obuch_med" {
		t.Fatalf("тип записи = %q, ожидался obuch_med", payload.PostType)
	}
	if payload.CategoryTaxonomy != "obuch_med-cat" {
		t.Fatalf("таксономия рубрики = %q, ожидалась obuch_med-cat", payload.CategoryTaxonomy)
	}
	if payload.CategoryID != 1106951 {
		t.Fatalf("рубрика = %d", payload.CategoryID)
	}
	if len(payload.TagIDs) != 0 {
		t.Fatalf("странице услуги проставлены метки: %v", payload.TagIDs)
	}
	// Рубрика ищется в своей таксономии, а не во встроенной category: та к этому типу
	// записи не привязана, и поиск в ней нашёл бы либо не то, либо ничего.
	if len(client.termLookups) != 1 || client.termLookups[0] != courseCategoryTaxonomy+"/Обучение медперсонала" {
		t.Fatalf("рубрика искалась не там: %v", client.termLookups)
	}
	if plan.CategoryName != "Обучение медперсонала" {
		t.Fatalf("имя рубрики в отчёте = %q", plan.CategoryName)
	}
	// Преподаватель ищется в своём типе записи — том же, каким заведена запись 21785.
	if len(client.postLookups) != 1 || client.postLookups[0] != "teacher/Соколовская Елена Романовна" {
		t.Fatalf("преподаватель искался не там: %v", client.postLookups)
	}
}

// Тело записи — файл article.html целиком и побайтово: из result.md HTML не берут.
func TestCourseMappingSendsArticleHTMLAsIs(t *testing.T) {
	deps, _, _, _ := newCoursePublishDeps()
	payload, _, err := buildWordPressPayload(context.Background(), deps, "2", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	if payload.ContentHTML != testCourseHTML {
		t.Fatalf("тело записи = %q", payload.ContentHTML)
	}
	if payload.Title != "Лабораторное дело - дистанционное обучение" {
		t.Fatalf("заголовок записи = %q", payload.Title)
	}
}

// Раскладка полей: ACF, Yoast, связь с преподавателем и репитер вопросов.
func TestCourseMappingFieldsCoverACFAndYoast(t *testing.T) {
	deps, _, _, _ := newCoursePublishDeps()
	payload, _, err := buildWordPressPayload(context.Background(), deps, "2", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	fields := make(map[string]string, len(payload.Fields))
	for _, field := range payload.Fields {
		fields[field.Key] = field.Value
	}
	// Ключи здесь — имена полей ACF, а не их field_xxx: postmeta хранит значение под именем,
	// и XML-RPC пишет postmeta напрямую, минуя ACF. Имена сняты со страницы курса 22065.
	want := map[string]string{
		"prof_title": "Курсы [blue]лаборанта[/blue]",
		"prof_blue":  testCoursePriceText,
		// alt картинки — SEO-заголовок: так это поле заполнено на живой странице курса.
		"image_alt": "Лабораторное дело — обучение с ДОТ и записью в ФИС ФРДО",
		// Название профессии приходит своей колонкой книги, а не из метки: меток у страницы
		// услуги нет.
		"prof_name": "Лаборант",
		// Связь ACF получает голый идентификатор: сериализованный массив WordPress
		// сериализует повторно, и поле превращается в строку вместо массива.
		"teachers":              "21785",
		"_yoast_wpseo_focuskw":  "Лабораторное дело",
		"_yoast_wpseo_title":    "Лабораторное дело — обучение с ДОТ и записью в ФИС ФРДО",
		"_yoast_wpseo_metadesc": "Лабораторное дело: обучение с применением ДОТ.",
		// Репитер: счётчик строк и подполя строки — те самые значения, которые админка
		// показывает в форме заполненными.
		"faq_loop":                "1",
		"faq_loop_0_faq_question": "Какое образование нужно?",
		"faq_loop_0_faq_answer":   "Среднее профессиональное медицинское.",
	}
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("поле %s = %q, ожидалось %q", key, fields[key], value)
		}
	}
	// Поля статьи блога у страницы услуги не появляются: пустыми они означали бы «блок есть,
	// но он пустой», а этих блоков у неё нет вовсе.
	// Поля стадии info у страницы услуги не заполняются: TL;DR и времени чтения задача не
	// генерирует, а блок вопросов у неё лежит своим репитером, а не blog_faq.
	for _, absent := range []string{"blog_tldr", "blog_read", "blog_faq"} {
		if _, found := fields[absent]; found {
			t.Fatalf("у страницы услуги появилось поле статьи блога %s", absent)
		}
	}
}

// SEO-заголовок берётся из своей колонки, а не из названия страницы: у статьи блога своей
// колонки нет, и там на его месте название — здесь это была бы подмена данных.
func TestCourseMappingSEOTitleComesFromOwnColumn(t *testing.T) {
	deps, repository, _, _ := newCoursePublishDeps()
	repository.input.SEOTitle = "Свой SEO-заголовок"
	payload, _, err := buildWordPressPayload(context.Background(), deps, "2", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	for _, field := range payload.Fields {
		if field.Key == "_yoast_wpseo_title" && field.Value != "Свой SEO-заголовок" {
			t.Fatalf("_yoast_wpseo_title = %q", field.Value)
		}
	}
}

// Подписи вложения: alt — фокусный ключ (так он объявлен в листе), title — слаг картинки.
func TestCourseMappingImageCaptions(t *testing.T) {
	deps, _, _, _ := newCoursePublishDeps()
	_, plan, err := buildWordPressPayload(context.Background(), deps, "2", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	if plan.ImageAlt != "Лабораторное дело — обучение с ДОТ и записью в ФИС ФРДО" {
		t.Fatalf("alt вложения = %q", plan.ImageAlt)
	}
	if plan.ImageTitle != "laboratornoe-delo" {
		t.Fatalf("title вложения = %q", plan.ImageTitle)
	}
}

// alt картинки и фокусное ключевое слово — разные значения: первое SEO-заголовок, второе
// ключ. Совпадали бы они только случайно, и подмена одного другим видна лишь в блоге.
func TestCourseMappingImageAltIsNotKeyword(t *testing.T) {
	deps, _, _, _ := newCoursePublishDeps()
	payload, plan, err := buildWordPressPayload(context.Background(), deps, "2", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	if plan.ImageAlt == "Лабораторное дело" {
		t.Fatal("alt вложения равен фокусному ключу, а должен быть SEO-заголовком")
	}
	for _, field := range payload.Fields {
		if field.Key == "image_alt" && field.Value != plan.ImageAlt {
			t.Fatalf("ACF image_alt = %q, а подпись вложения = %q — значение одно",
				field.Value, plan.ImageAlt)
		}
	}
}

// Блок стоимости берётся из result.md, а не собирается заново: лист и блог обязаны показывать
// один и тот же текст.
func TestCourseMappingFailsWithoutPriceSection(t *testing.T) {
	deps, _, _, _ := newCoursePublishDeps()
	deps.writer = &fakeWPWriter{files: map[string]string{
		testCoursePath:   testCourseHTML,
		testCourseResult: "## HTML\n\n```text\n" + testCoursePath + "\n```\n",
	}}
	_, _, err := buildWordPressPayload(context.Background(), deps, "2", true)
	if err == nil || !strings.Contains(err.Error(), priceSection) {
		t.Fatalf("публикация без блока стоимости не отбита: %v", err)
	}
}

// Ненайденный преподаватель останавливает публикацию этой страницы и не заводит запись:
// связь ACF хранит идентификатор, а выдумать его нельзя.
func TestCourseMappingStopsOnUnknownTeacher(t *testing.T) {
	deps, _, client, _ := newCoursePublishDeps()
	client.teacherErr = &wordpress.ErrPostNotFound{
		PostType: courseTeachersPostType, Title: "Соколовская Елена Романовна",
	}
	_, _, err := buildWordPressPayload(context.Background(), deps, "2", true)
	if err == nil {
		t.Fatal("публикация с неизвестным преподавателем не отбита")
	}
	var notFound *wordpress.ErrPostNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("тип ошибки потерян: %v", err)
	}
	// Отказ по данным статьи, а не по площадке: полный прогон обязан различать их — от
	// этого зависит, выключать ли публикацию для остальных страниц.
	if wordpress.IsSystemFailure(err) {
		t.Fatalf("ненайденный преподаватель считается отказом площадки: %v", err)
	}
}

// Пустые seo_title и teachers — негодные данные страницы, и узнать об этом надо до первого
// запроса в блог.
func TestCourseMappingRequiresOwnFields(t *testing.T) {
	cases := map[string]struct {
		mutate func(*article.PublicationInput)
		want   string
	}{
		"нет seo_title": {func(i *article.PublicationInput) { i.SEOTitle = "" }, "seo_title"},
		"нет teachers":  {func(i *article.PublicationInput) { i.Teachers = "  " }, "teachers"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			deps, repository, client, _ := newCoursePublishDeps()
			testCase.mutate(&repository.input)
			_, _, err := buildWordPressPayload(context.Background(), deps, "2", true)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("ошибка %v не называет причину %q", err, testCase.want)
			}
			if len(client.termLookups) != 0 || len(client.postLookups) != 0 {
				t.Fatal("непригодная страница успела сходить в WordPress")
			}
		})
	}
}

// Связь ACF уходит голым идентификатором. Сериализованный массив, каким это поле лежит на
// странице, заполненной через админку, через XML-RPC не проходит: WordPress пропускает
// значение через maybe_serialize, и уже сериализованное сериализуется повторно — вместо
// массива в блоге оказывается строка. Проверено записью 22215.
func TestCourseMappingTeacherIsPlainID(t *testing.T) {
	deps, _, _, _ := newCoursePublishDeps()
	payload, _, err := buildWordPressPayload(context.Background(), deps, "2", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	for _, field := range payload.Fields {
		if field.Key != "teachers" {
			continue
		}
		if field.Value != "21785" {
			t.Fatalf("связь с преподавателем = %q, ожидался голый идентификатор", field.Value)
		}
		if strings.HasPrefix(field.Value, "a:") {
			t.Fatal("связь ушла сериализованным массивом — WordPress сериализует его повторно")
		}
	}
}

// Статья блога от появления второй раскладки не меняется: тип записи и таксономия остаются
// умолчаниями площадки, метки — на месте.
func TestBlogMappingKeepsDefaultTypeAndTaxonomy(t *testing.T) {
	deps, _, _, _, _ := newWPPublishDeps()
	payload, _, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	if payload.PostType != "" || payload.CategoryTaxonomy != "" {
		t.Fatalf("у статьи блога появились свои тип и таксономия: %q / %q",
			payload.PostType, payload.CategoryTaxonomy)
	}
	if len(payload.TagIDs) != 2 {
		t.Fatalf("метки статьи блога = %v", payload.TagIDs)
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
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

// ---------------------------------------------------------------------------
// Раскладка площадки без ACF: obuch_1.
// ---------------------------------------------------------------------------

// newPlainBlogPublishDeps — те же зависимости, что у статьи блога, но с раскладкой площадки
// без полей ACF. Вход намеренно общий: различаться обязана только раскладка.
func newPlainBlogPublishDeps() (wordPressPublishDeps, *fakeWPRepository, *fakeWPClient) {
	deps, repository, client, _, _ := newWPPublishDeps()
	deps.mapping = plainBlogWordPressMapping{}
	return deps, repository, client
}

// Главное требование раскладки: в запись уходят три ключа Yoast и ни одного поля ACF.
//
// Проверяются обе стороны — и что нужное есть, и что лишнего нет. Вторая половина важнее:
// поле, которого на площадке не существует, уходит молча и живёт в записи навсегда.
func TestPlainBlogMappingSendsOnlyYoastFields(t *testing.T) {
	deps, _, _ := newPlainBlogPublishDeps()

	payload, _, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}

	fields := make(map[string]string, len(payload.Fields))
	for _, field := range payload.Fields {
		fields[field.Key] = field.Value
	}
	want := map[string]string{
		"_yoast_wpseo_focuskw":  "разряды газосварщиков",
		"_yoast_wpseo_title":    "Разряды газосварщиков: категории и зарплата",
		"_yoast_wpseo_metadesc": "Какие категории существуют.",
	}
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("поле %s = %q, ожидалось %q", key, fields[key], value)
		}
	}
	if len(payload.Fields) != len(want) {
		t.Fatalf("полей в нагрузке %d, ожидалось %d: %v", len(payload.Fields), len(want), fields)
	}
	for _, absent := range []string{
		"prof_title", "prof_blue", "prof_name",
		"blog_tldr", "blog_read", "blog_faq", "blog_faq_1_question",
		"related_courses", "author_link",
	} {
		if _, found := fields[absent]; found {
			t.Fatalf("на площадку без ACF ушло поле %q", absent)
		}
	}
}

// Рубрика, метки, ярлык и подписи обложки остаются: без них статья на площадке не находится.
func TestPlainBlogMappingKeepsCategoryTagsAndImage(t *testing.T) {
	deps, _, client := newPlainBlogPublishDeps()

	payload, plan, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}

	if payload.PostType != "" || payload.CategoryTaxonomy != "" {
		t.Fatalf("у статьи блога появились свои тип и таксономия: %q / %q",
			payload.PostType, payload.CategoryTaxonomy)
	}
	if payload.CategoryID != 2575 || len(payload.TagIDs) != 2 {
		t.Fatalf("рубрика %d, метки %v", payload.CategoryID, payload.TagIDs)
	}
	if payload.Slug != "razryady-gazosvarshchikov" {
		t.Fatalf("ярлык записи = %q", payload.Slug)
	}
	if plan.ImageAlt != "Разряды газосварщиков: какие бывают" || plan.ImageTitle != "razryady-gazosvarshchikov" {
		t.Fatalf("подписи обложки: alt=%q title=%q", plan.ImageAlt, plan.ImageTitle)
	}
	// Карточка автора и связь с преподавателем на площадке не ищутся: типов записи нет.
	if len(client.postLookups) != 0 {
		t.Fatalf("раскладка ходила в блог за записями: %v", client.postLookups)
	}
}

// Картинка в тело не вставляется: на этой площадке её нет ни у одной статьи.
func TestPlainBlogMappingSkipsBodyImage(t *testing.T) {
	deps, _, _ := newPlainBlogPublishDeps()

	_, plan, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}

	if !plan.WithoutBodyImage {
		t.Fatal("раскладка не сняла вставку картинки в тело статьи")
	}
}

// Признак картинки в теле по умолчанию выключен: у соседних задач она обязана остаться.
func TestBlogMappingKeepsBodyImage(t *testing.T) {
	deps, _, _, _, _ := newWPPublishDeps()

	_, plan, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}

	if plan.WithoutBodyImage {
		t.Fatal("у статьи блога dpoprof пропала картинка в теле")
	}
}

// Статья без меток не уходит на площадку, где их и так не ставят: иначе она там не находится.
func TestPlainBlogMappingRequiresTags(t *testing.T) {
	deps, repository, client := newPlainBlogPublishDeps()
	repository.input.Tags = "  "

	_, _, err := buildWordPressPayload(context.Background(), deps, "16", true)

	if err == nil {
		t.Fatal("нагрузка собрана без меток")
	}
	if !strings.Contains(err.Error(), "метки") {
		t.Fatalf("ошибка не называет причину: %v", err)
	}
	if len(client.termLookups) != 0 || len(client.postLookups) != 0 {
		t.Fatalf("непригодная статья успела сходить в блог: %v / %v", client.termLookups, client.postLookups)
	}
}

// ---------------------------------------------------------------------------
// Страница услуги площадки без ACF: obuch_2.
// ---------------------------------------------------------------------------

// newPlainServicePublishDeps собирает публикацию страницы услуги второй площадки.
//
// Отличие от соседей ровно в раскладке и в двух колонках книги: тип записи и адрес
// фотографии на стоке. Сценарий публикации общий, и подменять его тут нечем.
func newPlainServicePublishDeps() (wordPressPublishDeps, *fakeWPRepository, *fakeWPClient) {
	deps, repository, client, _, _ := newWPPublishDeps()
	deps.mapping = plainServiceWordPressMapping{site: catalog.Obuchim()}
	// Признаки профиля obuch_2: стадии info нет, и FAQ задача не собирает вовсе — вопросы
	// остаются в теле страницы, потому что поля под них у площадки не существует.
	deps.withoutArticleMetadata = true
	deps.metadataFAQOnly = false
	repository.input.PostType = "rabprof"
	repository.input.SEOTitle = "Разряды газосварщиков — обучение и документы"
	repository.input.ImageSourceURL = "https://www.pexels.com/ru-ru/photo/34193466/"
	return deps, repository, client
}

// Страница ложится в свой тип записи из книги и в рубрику своей таксономии — cat_<тип>.
// Правило имени зеркально соседней площадке, и второй его копии рядом с публикацией нет.
func TestPlainServiceMappingUsesBookPostTypeAndMirroredTaxonomy(t *testing.T) {
	deps, _, client := newPlainServicePublishDeps()

	payload, plan, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	if payload.PostType != "rabprof" {
		t.Fatalf("тип записи = %q, ожидался rabprof из книги", payload.PostType)
	}
	if payload.CategoryTaxonomy != "cat_rabprof" {
		t.Fatalf("таксономия рубрики = %q, ожидалась cat_rabprof", payload.CategoryTaxonomy)
	}
	if len(client.termLookups) != 1 || client.termLookups[0] != "cat_rabprof/Сварка, слесарка и металлообработка" {
		t.Fatalf("рубрика искалась не там: %v", client.termLookups)
	}
	if plan.PostType != "rabprof" || plan.CategoryTaxonomy != "cat_rabprof" {
		t.Fatalf("сухой прогон показывает %q / %q", plan.PostType, plan.CategoryTaxonomy)
	}
	// Ни карточки автора, ни связи с преподавателем: типов записи под них у площадки нет.
	if len(client.postLookups) != 0 {
		t.Fatalf("раскладка ходила в блог за записями: %v", client.postLookups)
	}
}

// Меток страница услуги не публикует и не требует: колонки tags у задачи нет вовсе.
//
// Полей блога на этой площадке тоже нет, и отправленное молча остаётся в записи навсегда —
// проверяется поимённо. А вот поля программы отправляются: плашку параметров и аккордеон
// модулей площадка рисует из них, и до сих пор их заполнял человек в админке.
func TestPlainServiceMappingSendsNoTagsAndNoBlogFields(t *testing.T) {
	deps, repository, _ := newPlainServicePublishDeps()
	repository.input.Tags = ""

	payload, _, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана без меток: %v", err)
	}
	if len(payload.TagIDs) != 0 {
		t.Fatalf("странице услуги проставлены метки: %v", payload.TagIDs)
	}
	fields := make(map[string]string, len(payload.Fields))
	for _, field := range payload.Fields {
		fields[field.Key] = field.Value
	}
	want := map[string]string{
		"_yoast_wpseo_focuskw":  "разряды газосварщиков",
		"_yoast_wpseo_title":    "Разряды газосварщиков — обучение и документы",
		"_yoast_wpseo_metadesc": "Какие категории существуют.",
	}
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("поле %s = %q, ожидалось %q", key, fields[key], value)
		}
	}
	// Поля статьи блога: типов записи и полей под них у площадки нет вовсе.
	for _, absent := range []string{
		"prof_title", "prof_blue", "image_alt", "teachers",
		"faq_loop", "faq_loop_0_faq_question", "blog_tldr", "blog_faq", "related_courses",
	} {
		if _, found := fields[absent]; found {
			t.Fatalf("на площадку без полей блога ушло поле %q", absent)
		}
	}
}

// Программа собирается из готовой разметки — того самого текста, что уйдёт в блог. Второго
// источника у модулей быть не должно: спросив модель отдельно, получили бы второй набор,
// отличный от напечатанного на странице.
func TestPlainServiceMappingBuildsProgramFromPage(t *testing.T) {
	deps, repository, _ := newPlainServicePublishDeps()
	repository.input.Hours = "38 часов"
	repository.input.Duration = "от 2 недель"
	repository.input.Price = "от 5 000 ₽"
	repository.input.Document = "Свидетельство о профессии рабочего «Газосварщик»"
	repository.input.Profession = "Газосварщик"
	deps.writer = &fakeWPWriter{files: map[string]string{
		repository.input.HTMLPath: servicePageWithProgram,
		testWPResultPath:          testWPResultMD,
	}}

	payload, _, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	fields := make(map[string]string, len(payload.Fields))
	for _, field := range payload.Fields {
		fields[field.Key] = field.Value
	}
	want := map[string]string{
		"prog_moduli":           "2",
		"prog_moduli_zagolovok": "Программа обучения",
		"prog_moduli_0_tema":    "Нормативно-правовое регулирование",
		"prog_moduli_0_chasy":   "20",
		"prog_moduli_1_tema":    "Основы экскурсоведения",
		"prog_moduli_1_chasy":   "18",
		"prog_chasy":            "38",
		"prog_srok":             "от 2 недель",
		"novaya_czena":          "от 5 000 ₽",
		"prof_name":             "Газосварщик",
		"prog_dokument":         "svidetelstvo",
		"prog_attestaciya":      "exam",
	}
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("поле %s = %q, ожидалось %q", key, fields[key], value)
		}
	}
	if fields["prog_moduli_0_opisanie"] == "" {
		t.Fatalf("описание модуля не уехало в запись: %v", fields)
	}
}

// Часы модулей и объём программы — одно и то же число, названное дважды. Расхождение
// останавливает публикацию до единого запроса в блог: в записи его видно только глазами на
// самой странице, и на живых страницах площадки оно уже случалось.
func TestPlainServiceMappingStopsOnHoursMismatch(t *testing.T) {
	deps, repository, client := newPlainServicePublishDeps()
	repository.input.Hours = "144 часа"
	deps.writer = &fakeWPWriter{files: map[string]string{
		repository.input.HTMLPath: servicePageWithProgram,
		testWPResultPath:          testWPResultMD,
	}}

	_, _, err := buildWordPressPayload(context.Background(), deps, "16", true)

	if err == nil {
		t.Fatal("расхождение часов не остановило публикацию")
	}
	if !strings.Contains(err.Error(), "38") || !strings.Contains(err.Error(), "144") {
		t.Fatalf("в ошибке нет обоих чисел: %v", err)
	}
	if len(client.created) != 0 {
		t.Fatalf("в блоге создана запись при расхождении: %v", client.created)
	}
}

// Страница без раздела программы — законное состояние: аккордеон остаётся пустым, его
// дозаполняет человек, а плашка параметров уходит как была. Публикацию это не роняет.
func TestPlainServiceMappingSurvivesPageWithoutProgram(t *testing.T) {
	deps, repository, _ := newPlainServicePublishDeps()
	repository.input.Hours = "144 часа"

	payload, _, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("страница без программы уронила публикацию: %v", err)
	}
	for _, field := range payload.Fields {
		if field.Key == "prog_moduli" {
			t.Fatalf("счётчик модулей ушёл при пустой программе: %v", payload.Fields)
		}
	}
}

// Разметка страницы с программой: список без заголовков, часы в жирном зачине строки.
const servicePageWithProgram = `<h2 class="wp-block-heading">Детальная программа обучения</h2>` +
	`<ol class="wp-block-list">` +
	`<li><strong>Модуль 1. Нормативно-правовое регулирование — 20 часов.</strong> Законодательство отрасли.</li>` +
	`<li><strong>Модуль 2. Основы экскурсоведения — 18 часов.</strong> Структура маршрута.</li>` +
	`</ol>`

// Тип записи, которого у площадки нет, останавливает публикацию до единого запроса в блог:
// иначе рубрика искалась бы в таксономии, которой не существует, и отказ пришёл бы из сети.
func TestPlainServiceMappingStopsOnUnknownPostType(t *testing.T) {
	deps, repository, client := newPlainServicePublishDeps()
	repository.input.PostType = "obuch_med" // тип соседней площадки

	_, _, err := buildWordPressPayload(context.Background(), deps, "16", true)

	if err == nil {
		t.Fatal("нагрузка собрана с чужим типом записи")
	}
	if !strings.Contains(err.Error(), "obuch_med") || !strings.Contains(err.Error(), "rabprof") {
		t.Fatalf("ошибка не называет ни чужой тип, ни доступные: %v", err)
	}
	if len(client.termLookups) != 0 || len(client.postLookups) != 0 {
		t.Fatalf("раскладка успела сходить в блог: %v %v", client.termLookups, client.postLookups)
	}
}

// Пустой тип записи — та же остановка: колонку заполняет человек, и пустая клетка означает
// незаполненную книгу, а не «пусть площадка решит сама».
func TestPlainServiceMappingRequiresPostType(t *testing.T) {
	deps, repository, client := newPlainServicePublishDeps()
	repository.input.PostType = "  "

	_, _, err := buildWordPressPayload(context.Background(), deps, "16", true)

	if err == nil {
		t.Fatal("нагрузка собрана без типа записи")
	}
	if !strings.Contains(err.Error(), "post_type") {
		t.Fatalf("ошибка не называет колонку: %v", err)
	}
	if len(client.termLookups) != 0 {
		t.Fatalf("раскладка успела сходить в блог: %v", client.termLookups)
	}
}

// Картинка в теле есть, но стоит не в середине: на живых страницах площадки она идёт сразу
// за лидом и плашкой параметров, а середина коммерческой страницы приходится на модули.
func TestPlainServiceMappingPutsBodyImageAfterLead(t *testing.T) {
	deps, _, _ := newPlainServicePublishDeps()

	_, plan, err := buildWordPressPayload(context.Background(), deps, "16", true)
	if err != nil {
		t.Fatalf("нагрузка не собрана: %v", err)
	}
	if plan.WithoutBodyImage {
		t.Fatal("раскладка сняла картинку из тела страницы услуги")
	}
	if !plan.BodyImageBeforeFirstHeading {
		t.Fatal("картинка уйдёт в середину страницы, а не за лид и плашку")
	}
	if plan.BodyImageSourceURL != "https://www.pexels.com/ru-ru/photo/34193466/" {
		t.Fatalf("адрес фотографии на стоке = %q", plan.BodyImageSourceURL)
	}
}

// Блок картинки: классы темы, ссылка на конкретный кадр. Пустая колонка книги означает, что
// абзаца-ссылки нет вовсе — главная страница стока вместо кадра была бы выдуманной
// атрибуцией, а в опубликованной записи её уже не отличить от настоящей.
func TestPlainBodyImageBlockPrintsSourceOnlyWhenKnown(t *testing.T) {
	media := wordpress.UploadedMedia{AttachmentID: 14546, URL: "https://obuchim-specialista.ru/wp-content/uploads/foto.webp"}

	withSource := plainBodyImageBlock(media, "Мастер по ремонту кофемашин", "https://www.pexels.com/ru-ru/photo/34193466/")
	for _, want := range []string{
		`<figure class="wp-block-image size-large">`,
		`class="wp-image-14546"`,
		`alt="Мастер по ремонту кофемашин"`,
		`<p class="wp-block-paragraph">`,
		`rel="nofollow noopener noreferrer"`,
		`Источник изображения: `,
		`>Pexels</a>`,
	} {
		if !strings.Contains(withSource, want) {
			t.Fatalf("в блоке картинки нет %q: %s", want, withSource)
		}
	}
	if strings.Contains(withSource, "style=") {
		t.Fatalf("в блоке картинки инлайновый стиль: %s", withSource)
	}

	withoutSource := plainBodyImageBlock(media, "Мастер по ремонту кофемашин", "   ")
	if strings.Contains(withoutSource, "<p") {
		t.Fatalf("абзац-ссылка напечатан без адреса фотографии: %s", withoutSource)
	}
	if !strings.Contains(withoutSource, "<figure") {
		t.Fatalf("картинка потеряна вместе с ссылкой: %s", withoutSource)
	}
}

// Раскладку выбирает профиль, а не имя задачи, и признак страницы услуги без ACF стоит выше
// двух прежних: нулевые значения у соседей обязаны означать прежнее поведение.
func TestNewWordPressMappingPicksLayoutByProfile(t *testing.T) {
	for _, item := range []struct {
		name    string
		profile tasks.Profile
		want    wordPressMapping
	}{
		{name: "страница услуги без ACF", want: plainServiceWordPressMapping{site: catalog.Obuchim()},
			profile: tasks.Profile{PlainServicePages: true, CatalogSite: catalog.SiteObuchim}},
		{name: "страница услуги dpoprof", want: courseWordPressMapping{},
			profile: tasks.Profile{CommercialPages: true}},
		{name: "статья блога без ACF", want: plainBlogWordPressMapping{},
			profile: tasks.Profile{PlainBlogPages: true}},
		{name: "статья блога dpoprof", want: blogWordPressMapping{}, profile: tasks.Profile{}},
	} {
		t.Run(item.name, func(t *testing.T) {
			got := newWordPressMapping(item.profile)
			if reflect.TypeOf(got) != reflect.TypeOf(item.want) {
				t.Fatalf("раскладка %T, ожидалась %T", got, item.want)
			}
			if plain, ok := got.(plainServiceWordPressMapping); ok {
				if plain.siteErr != nil {
					t.Fatalf("площадка не разрешена: %v", plain.siteErr)
				}
				if plain.site.Key() != catalog.SiteObuchim {
					t.Fatalf("раскладка получила площадку %q", plain.site.Key())
				}
			}
		})
	}
}

// Опечатка в площадке профиля не теряется: раскладка собирается там, где возвращать ошибку
// некуда, поэтому отказ переносится в неё и поднимается первой же проверкой — до блога.
func TestPlainServiceMappingRaisesUnknownSite(t *testing.T) {
	mapping := newWordPressMapping(tasks.Profile{
		Command: "obuch-2", PlainServicePages: true, CatalogSite: "obuchim-specialista",
	})
	err := mapping.Validate(readyWPPublicationInput())
	if err == nil {
		t.Fatal("раскладка приняла несуществующую площадку")
	}
	if !strings.Contains(err.Error(), "obuchim-specialista") {
		t.Fatalf("ошибка не называет площадку: %v", err)
	}
}

// Сухой прогон обязан показывать, что раскладка сделает с обложкой внутри тела: по нагрузке
// этого не видно — картинки в ней нет, её адрес появляется только после загрузки вложения.
func TestWordPressPlanDescribesBodyImage(t *testing.T) {
	for _, item := range []struct {
		name string
		plan wordPressPayloadContext
		want string
	}{
		{name: "статья блога dpoprof", plan: wordPressPayloadContext{}, want: "серединным"},
		{name: "статья блога без картинок", plan: wordPressPayloadContext{WithoutBodyImage: true}, want: "не вставляется"},
		{
			name: "страница услуги со ссылкой на кадр",
			plan: wordPressPayloadContext{
				BodyImageBeforeFirstHeading: true,
				BodyImageSourceURL:          "https://www.pexels.com/ru-ru/photo/34193466/",
			},
			want: "https://www.pexels.com/ru-ru/photo/34193466/",
		},
		{
			name: "страница услуги без адреса фотографии",
			plan: wordPressPayloadContext{BodyImageBeforeFirstHeading: true},
			want: "без ссылки на источник",
		},
	} {
		t.Run(item.name, func(t *testing.T) {
			if got := wordPressPlanBodyImage(item.plan); !strings.Contains(got, item.want) {
				t.Fatalf("сухой прогон печатает %q, ожидалось упоминание %q", got, item.want)
			}
		})
	}
}

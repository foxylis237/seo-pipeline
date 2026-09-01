package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/result"
)

// Раскладка данных статьи по полям площадки.
//
// Это единственное, чем задачи отличаются при публикации. Всё остальное — клиент, транспорт,
// порядок шагов, загрузка обложки, отметка в PostgreSQL, обратная сверка, сухой прогон,
// массовый прогон, публикация после run и политика ошибок — общее и живёт в одном месте на
// все задачи. Второго publisher поэтому нет и заводить его нельзя: расходятся здесь данные,
// а не сценарий.
//
// Отображение принадлежит площадке, а не движку: ни одна стадия пайплайна про blog_tldr,
// prof_name и поля ACF не знает и знать не должна.

// wordPressMappedPost — то, во что задача превращает данные статьи.
//
// Возвращается целиком и одним куском: рубрика, метки, подписи вложения и поля разбирают
// одни и те же данные, и собирать их четырьмя вызовами значило бы четыре раза читать одно и
// то же и четыре раза ошибиться по-разному.
type wordPressMappedPost struct {
	// PostType — тип записи. Пустой означает обычную запись блога.
	PostType string
	// CategoryTaxonomy — таксономия рубрики. Пустая означает встроенную category.
	CategoryTaxonomy string
	// CategoryID — рубрика, уже разрешённая в идентификатор.
	CategoryID int64
	// CategoryName — её имя. Нужно сухому прогону: в нагрузке лежит число, а человеку перед
	// необратимой командой надо видеть, во что превратилось имя из книги.
	CategoryName string
	// Tags — метки в порядке из книги. Пустые у задачи, которая меток не публикует.
	Tags []wordpress.Tag
	// ImageAlt и ImageTitle — подписи вложения, уходящие вместе с файлом.
	ImageAlt   string
	ImageTitle string
	// Fields — ACF и Yoast одним списком.
	Fields []wordpress.CustomField
}

// wordPressMapping — раскладка одной задачи.
//
// Интерфейс объявлен у потребителя и держится узким намеренно: два метода — это ровно то,
// что публикация спрашивает у задачи, и добавление третьей задачи не трогает ни сценарий
// публикации, ни соседние раскладки.
type wordPressMapping interface {
	// Validate проверяет поля, обязательные именно этой раскладке.
	//
	// Общие для всех задач требования проверяет repository.ValidatePublicationInput; здесь
	// остаётся своё: метки нужны блоговой статье и не нужны странице услуги, seo_title и
	// преподаватель — наоборот.
	Validate(input article.PublicationInput) error

	// Build собирает раскладку. createMissingTerms разводит боевую публикацию и сухой
	// прогон: второй обязан остаться read-only и не заводить в чужом блоге ни одного термина.
	Build(
		ctx context.Context, deps wordPressPublishDeps, input article.PublicationInput,
		faqItems []result.FAQItem, readingTime string, createMissingTerms bool,
	) (wordPressMappedPost, error)
}

// ---------------------------------------------------------------------------
// Блоговая статья: task_1 и pprof_1.
// ---------------------------------------------------------------------------

// profBlueValue — содержимое синего блока со стоимостью у блоговой статьи. Значение одно на
// все статьи и задано человеком; из данных статьи оно не выводится и моделью не генерируется.
const profBlueValue = "от 7 000 р"

// blogWordPressMapping — раскладка полей темы dpoprof для статьи блога.
//
// Имена ключей сняты с живой записи, заполненной вручную, и повторяют её раскладку: репитер
// FAQ хранится плоскими ключами blog_faq_<N>_question и blog_faq_<N>_answer, а blog_faq —
// счётчиком строк, тоже текстом.
type blogWordPressMapping struct{}

func (blogWordPressMapping) Validate(input article.PublicationInput) error {
	if strings.TrimSpace(input.Tags) == "" {
		return fmt.Errorf("у статьи %s не заполнено обязательное для публикации поле: метки",
			input.Article.ExternalID)
	}
	return nil
}

func (m blogWordPressMapping) Build(
	ctx context.Context, deps wordPressPublishDeps, input article.PublicationInput,
	faqItems []result.FAQItem, readingTime string, createMissingTerms bool,
) (wordPressMappedPost, error) {
	externalID := input.Article.ExternalID
	categoryID, err := deps.client.FindCategoryID(ctx, input.Category)
	if err != nil {
		return wordPressMappedPost{}, fmt.Errorf("рубрика статьи %s: %w", externalID, err)
	}
	tagNames := wordpress.SplitTermNames(input.Tags)
	if len(tagNames) == 0 {
		return wordPressMappedPost{}, fmt.Errorf("у статьи %s не разобраны метки: %q", externalID, input.Tags)
	}
	tags, err := resolveWordPressTags(ctx, deps, externalID, tagNames, createMissingTerms)
	if err != nil {
		return wordPressMappedPost{}, err
	}
	fields := blogCustomFields(input, faqItems, readingTime, tagNames, !deps.withoutArticleMetadata)
	if authorID := resolveBlogAuthor(ctx, deps, externalID, input.Author); authorID != 0 {
		fields = append(fields, wordpress.CustomField{
			Key: blogFieldAuthor, Value: strconv.FormatInt(authorID, 10),
		})
	}
	return wordPressMappedPost{
		CategoryID:   categoryID,
		CategoryName: strings.TrimSpace(input.Category),
		Tags:         tags,
		// alt — заголовок H1 статьи, title — слаг картинки. Оба значения приходят из
		// PostgreSQL как есть и из загруженного файла не выводятся.
		ImageAlt:   strings.TrimSpace(input.Header),
		ImageTitle: strings.TrimSpace(input.Article.Slug),
		Fields:     fields,
	}, nil
}

// blogFieldAuthor — связь записи блога с карточкой автора (ACF, значение — идентификатор
// записи типа author). Скаляр, а не сериализованный массив: WordPress пропускает значение
// через maybe_serialize, и уже сериализованная строка сериализуется повторно, а скаляр ACF
// разворачивает сама. Ровно так же устроена связь с преподавателем у pprof_2.
const blogFieldAuthor = "author_link"

// blogAuthorPostType — тип записей, которыми заведены авторы блога.
//
// Это НЕ teacher: карточки преподавателей курсов лежат отдельным типом, и у блога свой.
// Проверено на записи 21607 — её author_link ведёт на запись 18279 типа author.
const blogAuthorPostType = "author"

// resolveBlogAuthor выбирает первого автора статьи, у которого есть карточка на сайте.
//
// В книге импорта колонка перечисляет нескольких человек через запятую, и карточка есть не у
// каждого: из трёх авторов статьи 17 на сайте заведены двое. Поэтому берётся первый
// найденный, а не первый в списке.
//
// Порядок слов в книге и на сайте разный: в Excel «Вячеслав Борисович Воинов», на сайте
// «Воинов Вячеслав Борисович». Поэтому имя ищется в обоих видах.
//
// Отсутствие карточки публикацию не роняет: связь автора — украшение записи, а не условие её
// существования, и статья без него уходит в блог как раньше. Причина остаётся в логе.
func resolveBlogAuthor(
	ctx context.Context, deps wordPressPublishDeps, externalID, authors string,
) int64 {
	for _, name := range wordpress.SplitTermNames(authors) {
		for _, variant := range authorNameVariants(name) {
			id, err := deps.client.FindPostIDByTitle(ctx, blogAuthorPostType, variant)
			if err == nil && id != 0 {
				deps.logger.Info("автор статьи найден", "external_id", externalID,
					"stage", "wordpress_publish", "author", variant, "author_post_id", id)
				return id
			}
		}
	}
	if strings.TrimSpace(authors) != "" {
		deps.logger.Warn("карточка автора не найдена, связь не заполняется",
			"external_id", externalID, "stage", "wordpress_publish", "authors", authors)
	}
	return 0
}

// authorNameVariants возвращает написания ФИО, под которыми карточка может быть заведена.
//
// Первым идёт «Фамилия Имя Отчество» — так названы карточки на сайте; вторым исходное
// написание из книги, на случай если порядок там уже правильный.
func authorNameVariants(name string) []string {
	name = strings.TrimSpace(name)
	parts := strings.Fields(name)
	if len(parts) != 3 {
		return []string{name}
	}
	return []string{parts[2] + " " + parts[0] + " " + parts[1], name}
}

// blogCustomFields раскладывает данные статьи по полям темы dpoprof.
func blogCustomFields(
	input article.PublicationInput, faqItems []result.FAQItem, readingTime string, tagNames []string,
	withBlogMetadata bool,
) []wordpress.CustomField {
	fields := make([]wordpress.CustomField, 0, 9+2*len(faqItems))
	// Поля блоговой статьи. Пустыми их не отправляют: в теме они означают «блок есть, но
	// он пустой», и у страницы, которая этих блоков не имеет, их не должно быть вовсе.
	if withBlogMetadata {
		fields = append(fields,
			wordpress.CustomField{Key: "blog_tldr", Value: strings.TrimSpace(input.TLDR)},
			wordpress.CustomField{Key: "blog_read", Value: readingTime},
		)
	}
	// Счётчик строк репитера идёт вместе с самими вопросами, а не с полями блога: у задачи,
	// которая даёт только FAQ, блок вопросов на странице есть, а TL;DR и времени чтения нет.
	if len(faqItems) > 0 {
		fields = append(fields, wordpress.CustomField{Key: "blog_faq", Value: strconv.Itoa(len(faqItems))})
	}
	for index, item := range faqItems {
		fields = append(fields,
			wordpress.CustomField{Key: fmt.Sprintf("blog_faq_%d_question", index), Value: item.Question},
			wordpress.CustomField{Key: fmt.Sprintf("blog_faq_%d_answer", index), Value: item.Answer},
		)
	}
	return append(fields,
		wordpress.CustomField{Key: "prof_title", Value: strings.TrimSpace(input.Header)},
		wordpress.CustomField{Key: "prof_blue", Value: profBlueValue},
		wordpress.CustomField{Key: "prof_name", Value: professionName(tagNames)},
		wordpress.CustomField{Key: "_yoast_wpseo_focuskw", Value: strings.TrimSpace(input.Keyword)},
		wordpress.CustomField{Key: "_yoast_wpseo_title", Value: strings.TrimSpace(input.Article.Title)},
		wordpress.CustomField{Key: "_yoast_wpseo_metadesc", Value: strings.TrimSpace(input.MetaDescription)},
	)
}

// professionName берёт название профессии из первой метки.
//
// Правило проверено на одиннадцати записях, заполненных вручную: первая метка совпала с
// проставленным человеком prof_name в десяти из них, а единственное расхождение —
// «Кассир-операционист» против «кассир операционист» — только в дефисе. Ключевой запрос для
// этой роли не годится принципиально: там «разряды газосварщиков», а не профессия.
func professionName(tagNames []string) string {
	if len(tagNames) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(tagNames[0]))
}

// ---------------------------------------------------------------------------
// Коммерческая страница услуги: pprof_2.
// ---------------------------------------------------------------------------

// Идентификаторы площадки, которыми страница услуги отличается от статьи блога.
//
// Собраны в одном месте намеренно: это не логика, а справочник конкретной установки
// WordPress — имена таксономии, типа записи и полей ACF. Меняются они правкой этого блока,
// а не поиском по коду.
//
// Значения сняты с существующей страницы курса (запись 22065) и записи её преподавателя
// (21785) чтением wp.getPost, а не выведены из разметки админки. Разница принципиальная:
// в форме видны КЛЮЧИ полей ACF (name="acf[field_636b3ef2fac08]"), а значение лежит в
// postmeta под ИМЕНЕМ поля. XML-RPC пишет postmeta напрямую, минуя ACF, поэтому здесь нужны
// имена — как и у блоговой раскладки, где стоят prof_title и blog_faq.
const (
	// coursePostType — тип записи страницы услуги. Не "post": страницы курсов заведены
	// собственным типом, и таксономия рубрики привязана именно к нему.
	coursePostType = "obuch_med"
	// courseCategoryTaxonomy — таксономия рубрики страницы услуги.
	courseCategoryTaxonomy = "obuch_med-cat"
	// courseTeachersPostType — тип записи преподавателей: по имени из книги в нём ищется
	// запись, идентификатор которой уходит в связь ACF. В единственном числе, как на площадке.
	courseTeachersPostType = "teacher"

	// courseFieldTeachers — связь с преподавателем (ключ field_course_teachers).
	//
	// Значение — голый идентификатор записи, а не сериализованный PHP-массив, хотя на живой
	// странице поле лежит именно массивом: так его пишет сама ACF. Через XML-RPC повторить
	// это нельзя — WordPress пропускает значение через maybe_serialize, а тот сериализует
	// повторно всё, что уже похоже на сериализованное. Проверено записью 22215: ушло
	// a:1:{i:0;s:5:"18220";}, легло s:22:"a:1:{i:0;s:5:"18220";}";, то есть строка вместо
	// массива. Скаляр же ACF разворачивает в массив сама (acf_get_array), и связь читается.
	courseFieldTeachers = "teachers"
	// courseFieldFAQ — репитер частых вопросов (ключ field_69e70ae9149fe): значение — число
	// строк, сами строки лежат ключами faq_loop_<N>_faq_question и faq_loop_<N>_faq_answer.
	courseFieldFAQ = "faq_loop"
	// courseFieldFAQQuestion и courseFieldFAQAnswer — подполя строки репитера
	// (ключи field_69e70b00149ff и field_69e70b3014a00).
	courseFieldFAQQuestion = "faq_question"
	courseFieldFAQAnswer   = "faq_answer"
	// Ключи полей ACF. Уходят ссылками на определения: рядом со значением ACF держит
	// _<имя> = <ключ поля>.
	//
	// Обычным текстовым полям ссылка не нужна: prof_title, prof_blue, image_alt и prof_name
	// на опубликованной странице отрисовались и без неё. Репитеру и связи нужна — без ссылки
	// have_rows не находит поле, и блок вопросов не выводится вовсе, хотя строки в postmeta
	// лежат (записи 22215 и 22220).
	//
	// Сегодня площадка эти ссылки не принимает: ключи с ведущим подчёркиванием XML-RPC
	// отбрасывает. Измерено записью _thumbnail_id в черновик — обложка не появилась, тогда
	// как обычное поле в том же запросе легло. Ключи Yoast проходят потому, что защиту с них
	// снимает сам Yoast. Ссылки всё равно отправляются: они верны по составу и заработают в
	// тот день, когда площадка разрешит эти ключи, — а до тех пор молча отбрасываются, и
	// сверка их не проверяет (Unreadable).
	// courseFieldImageAlt — альтернативный текст главной картинки (ключ field_63d8f9f73f024).
	courseFieldImageAlt = "image_alt"
	// courseFieldHeader — заголовок курса с разметкой [blue]…[/blue]
	// (ключ field_636b3ef2fac08). Имя то же, что и у статьи блога: поле общее для темы.
	courseFieldHeader = "prof_title"
	// courseFieldPrice — блок стоимости и условий обучения (ключ field_636b3f8dfac09).
	// Имя тоже общее с блоговой раскладкой, отличается только источник значения.
	courseFieldPrice = "prof_blue"
	// courseFieldProfession — название профессии. Имя общее с блоговой раскладкой, но
	// источник свой: у страницы услуги это колонка profession книги, а не первая метка —
	// меток у неё нет вовсе.
	courseFieldProfession = "prof_name"
)

// priceSection — заголовок раздела result.md, откуда берётся блок стоимости.
//
// Значение не собирается заново при публикации: оно уже есть в result.md, человек его там
// видел, и второй сборкой того же текста разошлись бы лист и блог. Ровно тот же приём, что
// у времени чтения блоговой статьи.
const priceSection = "## Синий блок со стоимостью"

// courseWordPressMapping — раскладка полей страницы услуги.
type courseWordPressMapping struct{}

func (courseWordPressMapping) Validate(input article.PublicationInput) error {
	required := []struct {
		name  string
		value string
	}{
		{"SEO-заголовок (seo_title)", input.SEOTitle},
		{"преподаватель (teachers)", input.Teachers},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("у статьи %s не заполнено обязательное для публикации поле: %s",
				input.Article.ExternalID, field.name)
		}
	}
	// Меток страница услуги не публикует: колонки tags у задачи нет вовсе, и требовать их
	// значило бы не опубликовать её никогда.
	return nil
}

func (m courseWordPressMapping) Build(
	ctx context.Context, deps wordPressPublishDeps, input article.PublicationInput,
	faqItems []result.FAQItem, _ string, _ bool,
) (wordPressMappedPost, error) {
	externalID := input.Article.ExternalID
	categoryID, err := deps.client.FindTermIDInTaxonomy(ctx, courseCategoryTaxonomy, input.Category)
	if err != nil {
		return wordPressMappedPost{}, fmt.Errorf("рубрика статьи %s: %w", externalID, err)
	}
	// Преподаватель разрешается в идентификатор записи до всякой записи в блог: связь ACF
	// хранит число, а не имя, и ненайденный преподаватель обязан остановить публикацию
	// здесь, а не оставить страницу без блока. Заводить записи преподавателей запрещено.
	teacherID, err := deps.client.FindPostIDByTitle(ctx, courseTeachersPostType, input.Teachers)
	if err != nil {
		return wordPressMappedPost{}, fmt.Errorf("преподаватель статьи %s: %w", externalID, err)
	}
	priceBlock, err := resultSectionValue(deps.writer, input.HTMLPath, priceSection)
	if err != nil {
		return wordPressMappedPost{}, err
	}

	// Альтернативный текст картинки — SEO-заголовок. Значение снято с живой страницы курса
	// (image_alt = seo_title), а не выведено из листа result.md, где в этом разделе стоит
	// фокусное ключевое слово. Одно и то же значение уходит и подписью вложения, и в поле
	// ACF: это одна подпись, а не две.
	imageAlt := strings.TrimSpace(input.SEOTitle)
	return wordPressMappedPost{
		PostType:         coursePostType,
		CategoryTaxonomy: courseCategoryTaxonomy,
		CategoryID:       categoryID,
		CategoryName:     strings.TrimSpace(input.Category),
		ImageAlt:         imageAlt,
		ImageTitle:       strings.TrimSpace(input.Article.Slug),
		Fields:           courseCustomFields(input, faqItems, teacherID, priceBlock, imageAlt),
	}, nil
}

// courseCustomFields раскладывает данные страницы услуги по полям ACF и Yoast.
func courseCustomFields(
	input article.PublicationInput, faqItems []result.FAQItem,
	teacherID int64, priceBlock, imageAlt string,
) []wordpress.CustomField {
	fields := make([]wordpress.CustomField, 0, 8+2*len(faqItems))
	// Преподаватель у страницы один, и уходит он одним идентификатором — см. комментарий к
	// courseFieldTeachers о том, почему сериализованный массив здесь не проходит.
	fields = append(fields,
		wordpress.CustomField{Key: courseFieldTeachers, Value: strconv.FormatInt(teacherID, 10)},
	)
	if len(faqItems) > 0 {
		fields = append(fields, wordpress.CustomField{Key: courseFieldFAQ, Value: strconv.Itoa(len(faqItems))})
	}
	for index, item := range faqItems {
		fields = append(fields,
			wordpress.CustomField{
				Key:   fmt.Sprintf("%s_%d_%s", courseFieldFAQ, index, courseFieldFAQQuestion),
				Value: item.Question,
			},
			wordpress.CustomField{
				Key:   fmt.Sprintf("%s_%d_%s", courseFieldFAQ, index, courseFieldFAQAnswer),
				Value: item.Answer,
			},
		)
	}
	return append(fields,
		wordpress.CustomField{Key: courseFieldHeader, Value: strings.TrimSpace(input.Header)},
		wordpress.CustomField{Key: courseFieldPrice, Value: priceBlock},
		wordpress.CustomField{Key: courseFieldImageAlt, Value: imageAlt},
		// Название профессии берётся колонкой книги как есть. Ни из заголовка, ни из ключа
		// оно не выводится: у статьи блога такой колонки нет и там приходится угадывать по
		// первой метке, здесь угадывать нечего — значение написано человеком.
		wordpress.CustomField{Key: courseFieldProfession, Value: strings.TrimSpace(input.Profession)},
		wordpress.CustomField{Key: "_yoast_wpseo_focuskw", Value: strings.TrimSpace(input.Keyword)},
		// SEO-заголовок у страницы услуги свой, отдельной колонкой книги: у блоговой статьи
		// на его месте название статьи, потому что своей колонки у неё нет.
		wordpress.CustomField{Key: "_yoast_wpseo_title", Value: strings.TrimSpace(input.SEOTitle)},
		wordpress.CustomField{Key: "_yoast_wpseo_metadesc", Value: strings.TrimSpace(input.MetaDescription)},
	)
}

// newWordPressMapping выбирает раскладку по профилю задачи.
//
// Признак берётся из профиля, а не из имени задачи: движок имён задач не знает, а
// composition root уже держит их список. Задача без своей раскладки публикуется как статья
// блога — так работали task_1 и pprof_1 до появления второй раскладки.
func newWordPressMapping(commercial bool) wordPressMapping {
	if commercial {
		return courseWordPressMapping{}
	}
	return blogWordPressMapping{}
}

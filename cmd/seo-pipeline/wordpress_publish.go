package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/result"
)

const (
	// wordPressPublishOperation публикует статью. Без идентификатора — все подходящие.
	wordPressPublishOperation = "publish"
	// wordPressMarkPublishedOperation ставит отметку, не обращаясь к WordPress.
	wordPressMarkPublishedOperation = "mark-published"
)

// wordPressPublishDeadline — потолок публикации одной статьи вместе со справочниками и
// обратной сверкой. Стоит явно, а не складывается из вложенных бюджетов: перемножение
// таймаутов этот проект уже проходил (ловушка H6).
// Загрузка обложки укладывается в свой бюджет внутри интеграции; здесь потолок общий и
// учитывает её: у картинки вес мегабайтами, и четырёх минут на файл, справочники, запись и
// обратную сверку не хватило бы.
const wordPressPublishDeadline = 10 * time.Minute

// profBlueValue — содержимое синего блока со стоимостью. Значение одно на все статьи и
// задано человеком; из данных статьи оно не выводится и моделью не генерируется.
const profBlueValue = "от 7 000 р"

// readingTimeSection — заголовок раздела result.md, откуда берётся время чтения.
//
// Значение не пересчитывается при публикации: в блог уходит ровно то, что уже собрано в
// result.md и что человек там видел.
const readingTimeSection = "## Время чтения"

// wordPressPublishClient — то, что публикация требует от интеграции.
//
// Интерфейс объявлен у потребителя и держится узким: благодаря этому весь поток, включая
// разбор отказов и порядок записи в БД, проверяется без живого сайта.
type wordPressPublishClient interface {
	FindCategoryID(ctx context.Context, name string) (int64, error)
	FindTagID(ctx context.Context, name string) (int64, error)
	EnsureTag(ctx context.Context, name string) (wordpress.Tag, error)
	UploadMedia(ctx context.Context, file wordpress.MediaFile) (wordpress.UploadedMedia, error)
	CreatePost(ctx context.Context, payload wordpress.PostPayload) (int64, error)
	GetPost(ctx context.Context, postID int64) (wordpress.StoredPost, error)
}

// wordPressImageSource — обложка статьи на диске.
//
// Два шага вместо одного намеренно: сухой прогон обязан сказать, какой файл уйдёт и сколько
// он весит, ни разу не подняв в память двадцать мегабайт, а публикация читает файл ровно
// один раз и непосредственно перед отправкой.
type wordPressImageSource interface {
	Locate(externalID, slug string) (wordPressImage, error)
	Load(image wordPressImage) (wordpress.MediaFile, error)
}

// wordPressPublishRepository — состояние публикации в PostgreSQL.
type wordPressPublishRepository interface {
	GetPublicationInput(ctx context.Context, externalID string) (article.PublicationInput, error)
	ListPublishable(ctx context.Context) ([]string, error)
	SavePublication(ctx context.Context, externalID string, postID int64, url string) error
	LinkPublication(ctx context.Context, externalID string, postID int64, url string) error
}

// wordPressPublishWriter — доступ к артефактам статьи.
type wordPressPublishWriter interface {
	Read(relativePath string) (string, error)
	Exists(relativePath string) bool
}

// wordPressResultBuilder пересобирает result.md, чтобы в нём появилась ссылка на запись.
type wordPressResultBuilder interface {
	Build(ctx context.Context, externalID string) (articleoutput.ArticlePaths, error)
}

type wordPressPublishDeps struct {
	client      wordPressPublishClient
	repository  wordPressPublishRepository
	writer      wordPressPublishWriter
	images      wordPressImageSource
	resultBuild wordPressResultBuilder
	logger      *slog.Logger
	out         io.Writer
	assumeYes   bool
	interactive bool
	in          io.Reader
}

// runWordPressPublish публикует одну статью.
//
// Порядок шагов — это и есть защита от дублей, менять его нельзя:
//
//  1. всё проверяется локально, без единого запроса в WordPress;
//  2. рубрика и метки разрешаются в идентификаторы. Ненайденная рубрика останавливает
//     публикацию: заводить их приложению нельзя. Ненайденная метка заводится здесь же —
//     это первый записывающий запрос в блог, и стоит он после всех локальных проверок
//     намеренно: непригодная статья не должна оставлять в блоге новых меток;
//  3. обложка уходит в медиабиблиотеку — до записи, потому что wp.newPost принимает у неё
//     только идентификатор, а назначить картинку вторым запросом нельзя: редактирование
//     записей запрещено. Отказ здесь оставляет блог без единой записи;
//  4. wp.newPost, ровно один, без повторов;
//  5. post_id немедленно уходит в PostgreSQL — раньше сверки, потому что запись уже создана,
//     удалить её нельзя, и незаписанный идентификатор означал бы дубль при следующем запуске;
//  6. запись читается обратно и сверяется по всем полям, включая обложку;
//  7. result.md пересобирается best-effort.
func runWordPressPublish(ctx context.Context, deps wordPressPublishDeps, externalID string) error {
	started := time.Now()
	logger := deps.logger.With("external_id", externalID, "stage", "wordpress_publish")

	payload, plan, err := buildWordPressPayload(ctx, deps, externalID, true)
	if err != nil {
		return err
	}

	logger.Info("публикация начата",
		"category_id", payload.CategoryID, "tag_ids", payload.TagIDs, "fields", len(payload.Fields),
		"image", plan.Image.Path)

	// Обложка уходит первой из записывающих операций. Порядок вынужденный: wp.newPost
	// принимает у картинки только идентификатор вложения, а дописать её к готовой записи
	// нельзя — редактирование записей запрещено. Отказ на этом шаге оставляет блог без
	// записи вовсе, то есть в состоянии, из которого повтор безопасен.
	attachmentID, err := uploadWordPressCover(ctx, deps, externalID, plan)
	if err != nil {
		return err
	}
	payload.ThumbnailID = attachmentID

	postID, err := deps.client.CreatePost(ctx, payload)
	if err != nil {
		// Повтора нет и не будет: неизвестно, дошёл ли запрос. Человеку нужно посмотреть в
		// админку, а не получить второй пост.
		logger.Error("публикация не удалась",
			"error", err, "duration_ms", time.Since(started).Milliseconds())
		return fmt.Errorf("создать запись WordPress для статьи %s: %w\n"+
			"Повтор автоматически не выполняется: неизвестно, была ли запись создана.\n"+
			"Дальше: проверьте блог вручную. Если запись появилась — отметьте её\n"+
			"командой mark-published %s, иначе следующий запуск создаст дубль",
			externalID, err, externalID)
	}
	logger.Info("запись создана", "post_id", postID, "duration_ms", time.Since(started).Milliseconds())

	// Отметка ставится до сверки. Это не небрежность: с этого момента повторная публикация
	// статьи запрещена в любом случае, чем бы сверка ни кончилась.
	if saveErr := deps.repository.SavePublication(ctx, externalID, postID, ""); saveErr != nil {
		logger.Error("отметку о публикации сохранить не удалось", "post_id", postID, "error", saveErr)
		fmt.Fprintf(deps.out, "\nВНИМАНИЕ: запись %d создана, но отметка в базе не сохранена.\n"+
			"Выполните: make <задача> mark-published %s — иначе следующий запуск создаст дубль.\n",
			postID, externalID)
		return fmt.Errorf("сохранить отметку о публикации статьи %s: %w", externalID, saveErr)
	}

	stored, err := deps.client.GetPost(ctx, postID)
	if err != nil {
		logger.Error("запись создана, но прочитать её обратно не удалось", "post_id", postID, "error", err)
		return fmt.Errorf("запись %d создана, но сверить её не удалось: %w", postID, err)
	}
	if mismatches := payload.Verify(stored); len(mismatches) > 0 {
		logger.Error("запись создана, но сверка не сошлась",
			"post_id", postID, "mismatches", len(mismatches),
			"duration_ms", time.Since(started).Milliseconds())
		var report strings.Builder
		for _, mismatch := range mismatches {
			fmt.Fprintf(&report, "\n  - %s", mismatch)
		}
		// Тип, а не fmt.Errorf: это отказ площадки, а не негодные данные статьи, и полный
		// прогон обязан различать их — от этого зависит, выключать ли публикацию дальше.
		return &wordpress.ResponseError{
			Endpoint: "wp.newPost",
			Message: fmt.Sprintf("запись %d создана, но сохранилось не всё — публикация неуспешна:%s\n"+
				"Запись не переписывается и не удаляется, отметка в базе оставлена: без неё\n"+
				"следующий запуск создал бы дубль. Разберитесь в админке вручную",
				postID, report.String()),
		}
	}

	if saveErr := deps.repository.SavePublication(ctx, externalID, postID, stored.Link); saveErr != nil {
		logger.Warn("адрес записи сохранить не удалось", "post_id", postID, "error", saveErr)
	}

	logger.Info("публикация завершена",
		"post_id", postID, "url", stored.Link,
		"result", "ok", "duration_ms", time.Since(started).Milliseconds())
	fmt.Fprintf(deps.out, "Статья %s опубликована: %s (запись %d)\n", externalID, stored.Link, postID)

	// Пересборка result.md — побочный шаг, и уронить публикацию он права не имеет: запись уже
	// создана, а ссылку всегда доберёт обычный result. Тот же принцип, что у публикации
	// промпта в Google Docs.
	if _, buildErr := deps.resultBuild.Build(ctx, externalID); buildErr != nil {
		logger.Warn("result.md не пересобран, ссылка появится после make <задача> result",
			"error", buildErr)
	}
	return nil
}

// uploadWordPressCover кладёт обложку статьи в медиабиблиотеку и возвращает вложение.
//
// Идентификатор нигде не сохраняется: он нужен ровно на время следующего вызова. Хранить его
// в PostgreSQL значило бы завести вторую отметку о публикации, которая расходится с первой —
// вложение без записи в блоге не видно и ценности не имеет, а связь «статья ⇄ запись» уже
// хранится в articles.wordpress_post_id.
func uploadWordPressCover(
	ctx context.Context, deps wordPressPublishDeps, externalID string, plan wordPressPayloadContext,
) (int64, error) {
	image := plan.Image
	file, err := deps.images.Load(image)
	if err != nil {
		return 0, err
	}
	// Подписи берутся из PostgreSQL и уходят тем же запросом, что и файл. Ни alt, ни title
	// не выводятся из имени файла и не дописываются после загрузки: вложение с пустым alt —
	// это картинка без альтернативного текста в уже опубликованной статье, а исправить её
	// приложение не сможет.
	file.AltText = plan.ImageAlt
	file.Title = plan.ImageTitle

	media, err := deps.client.UploadMedia(ctx, file)
	if err != nil {
		deps.logger.Error("обложка не загружена",
			"external_id", externalID, "stage", "wordpress_publish", "image", image.Path, "error", err)
		return 0, fmt.Errorf("загрузить обложку %s статьи %s: %w\n"+
			"Запись не создавалась, состояние статьи не изменилось — публикацию можно повторить.\n"+
			"Если обрыв случился после отправки, файл мог уже лечь в медиабиблиотеку: повтор\n"+
			"оставит там его копию, лишнюю удалите в админке",
			image.Path, externalID, err)
	}
	deps.logger.Info("обложка загружена",
		"external_id", externalID, "stage", "wordpress_publish",
		"attachment_id", media.AttachmentID, "url", media.URL,
		"bytes", len(file.Bits), "media_name", file.Name,
		"alt", media.AltText, "title", media.Title)
	return media.AttachmentID, nil
}

// buildWordPressPayload собирает нагрузку и проверяет всё, что можно проверить локально.
//
// createMissingTags разводит два вызова одной сборки. Боевая публикация заводит недостающие
// метки: без них запись не создать, а завести их заранее руками нельзя — имена приходят из
// Excel и меняются от статьи к статье. Сухой прогон передаёт false и остаётся полностью
// read-only: он обещает человеку, что в блоге ничего не изменится, и метка — тоже «что-то».
// Ненайденная метка в этом режиме получает нулевой идентификатор и попадает в отчёт как
// будущая, а не как отказ.
func buildWordPressPayload(
	ctx context.Context, deps wordPressPublishDeps, externalID string, createMissingTags bool,
) (wordpress.PostPayload, wordPressPayloadContext, error) {
	var plan wordPressPayloadContext
	input, err := deps.repository.GetPublicationInput(ctx, externalID)
	if err != nil {
		return wordpress.PostPayload{}, plan, err
	}
	if err := repository.ValidatePublicationInput(input); err != nil {
		return wordpress.PostPayload{}, plan, err
	}
	if !deps.writer.Exists(input.HTMLPath) {
		return wordpress.PostPayload{}, plan, fmt.Errorf(
			"у статьи %s нет файла %s — публиковать нечего", externalID, input.HTMLPath)
	}
	contentHTML, err := deps.writer.Read(input.HTMLPath)
	if err != nil {
		return wordpress.PostPayload{}, plan, fmt.Errorf("прочитать %s: %w", input.HTMLPath, err)
	}
	readingTime, err := readingTimeFromResult(deps.writer, input.HTMLPath)
	if err != nil {
		return wordpress.PostPayload{}, plan, fmt.Errorf("статья %s: %w", externalID, err)
	}
	faqItems, err := result.ParseFAQItems(input.FAQ)
	if err != nil {
		return wordpress.PostPayload{}, plan, fmt.Errorf("разобрать FAQ статьи %s: %w", externalID, err)
	}
	if len(faqItems) == 0 {
		return wordpress.PostPayload{}, plan, fmt.Errorf("у статьи %s пустой FAQ", externalID)
	}
	// Обложка ищется на диске, но не читается: годность статьи выясняется до единого запроса
	// в WordPress, а мегабайты картинки на этом шаге не нужны.
	image, err := deps.images.Locate(externalID, input.Article.Slug)
	if err != nil {
		return wordpress.PostPayload{}, plan, err
	}

	categoryID, err := deps.client.FindCategoryID(ctx, input.Category)
	if err != nil {
		return wordpress.PostPayload{}, plan, fmt.Errorf("рубрика статьи %s: %w", externalID, err)
	}
	tagNames := wordpress.SplitTermNames(input.Tags)
	if len(tagNames) == 0 {
		return wordpress.PostPayload{}, plan, fmt.Errorf("у статьи %s не разобраны метки: %q", externalID, input.Tags)
	}
	tags, err := resolveWordPressTags(ctx, deps, externalID, tagNames, createMissingTags)
	if err != nil {
		return wordpress.PostPayload{}, plan, err
	}
	tagIDs := make([]int64, 0, len(tags))
	for _, tag := range tags {
		// Ноль бывает только в сухом прогоне — у метки, которую заведёт публикация.
		// Идентификатора у неё пока нет, и класть его в нагрузку нечем.
		if tag.ID > 0 {
			tagIDs = append(tagIDs, tag.ID)
		}
	}

	plan = wordPressPayloadContext{
		Image:        image,
		ImageAlt:     strings.TrimSpace(input.Header),
		ImageTitle:   strings.TrimSpace(input.Article.Slug),
		CategoryName: strings.TrimSpace(input.Category),
		Tags:         tags,
		ReadingTime:  readingTime,
		FAQItems:     len(faqItems),
		HTMLPath:     input.HTMLPath,
		ContentRunes: len([]rune(contentHTML)),
	}
	return wordpress.PostPayload{
		Title:       strings.TrimSpace(input.Article.Title),
		ContentHTML: contentHTML,
		Status:      wordpress.PostStatusPublish,
		CategoryID:  categoryID,
		TagIDs:      tagIDs,
		Fields:      wordPressCustomFields(input, faqItems, readingTime, tagNames),
	}, plan, nil
}

// resolveWordPressTags превращает имена меток в идентификаторы.
//
// create разводит боевую публикацию и сухой прогон. В боевом режиме недостающая метка
// заводится: имена приходят колонкой Excel, их тысячи, и требовать от человека завести
// каждую заранее значило бы ронять публикацию на любой новой теме. Заведение — единственное
// место, где приложение создаёт термин, и рубрик оно не касается: тех два десятка, они
// продуманы человеком, и опечатка в них обязана останавливать публикацию.
//
// Дубли исключены на стороне интеграции (EnsureTag ищет до создания, а WordPress отвечает
// term_exists на гонку), поэтому повторная публикация одной и той же статьи новых меток
// не заводит.
func resolveWordPressTags(
	ctx context.Context, deps wordPressPublishDeps, externalID string, names []string, create bool,
) ([]wordpress.Tag, error) {
	tags := make([]wordpress.Tag, 0, len(names))
	for _, name := range names {
		if !create {
			id, err := deps.client.FindTagID(ctx, name)
			var notFound *wordpress.ErrTermNotFound
			switch {
			case err == nil:
				tags = append(tags, wordpress.Tag{ID: id, Name: name})
			case errors.As(err, &notFound):
				// Сухому прогону метка без идентификатора — не отказ, а строка отчёта:
				// публикация её заведёт, и человек имеет право увидеть это заранее.
				tags = append(tags, wordpress.Tag{Name: name})
			default:
				return nil, fmt.Errorf("метка статьи %s: %w", externalID, err)
			}
			continue
		}
		tag, err := deps.client.EnsureTag(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("метка статьи %s: %w", externalID, err)
		}
		if tag.Created {
			// Заведение термина в живом блоге человек обязан видеть в логе: удалять метки
			// приложение не умеет, и разбираться с опечаткой в Excel придётся руками.
			deps.logger.Info("метка заведена в WordPress",
				"external_id", externalID, "stage", "wordpress_publish",
				"tag", tag.Name, "tag_id", tag.ID)
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

// wordPressPayloadContext — то, чего в самой нагрузке уже не видно.
//
// Нужен сухому прогону: в PostPayload рубрика и метки лежат идентификаторами, а человеку
// перед необратимым действием надо показать, какие имена в них превратились.
type wordPressPayloadContext struct {
	// Image — найденная обложка. В нагрузке её нет и быть не может: туда попадает
	// идентификатор вложения, который появляется только после загрузки.
	Image wordPressImage
	// ImageAlt и ImageTitle — подписи вложения. Оба значения приходят из PostgreSQL как
	// есть: alt — заголовок H1 статьи (article_inputs.header), title — слаг картинки
	// (article_inputs.image_slug). Ни одно из них не выводится из загруженного файла и не
	// досочиняется после загрузки.
	ImageAlt     string
	ImageTitle   string
	CategoryName string
	// Tags — метки статьи в том порядке, в каком они перечислены в Excel. Нулевой
	// идентификатор бывает только в сухом прогоне и означает метку, которой на площадке
	// пока нет.
	Tags         []wordpress.Tag
	ReadingTime  string
	FAQItems     int
	HTMLPath     string
	ContentRunes int
}

// wordPressCustomFields раскладывает данные статьи по полям темы dpoprof.
//
// Имена ключей сняты с живой записи, заполненной вручную, и повторяют её раскладку: репитер
// FAQ хранится плоскими ключами blog_faq_<N>_question и blog_faq_<N>_answer, а blog_faq —
// счётчиком строк, тоже текстом.
//
// Отображение принадлежит площадке, а не движку: никакая стадия пайплайна про blog_tldr и
// prof_name не знает и знать не должна. Вторая площадка получит своё отображение здесь же,
// не трогая ни одного пакета internal/pipeline.
func wordPressCustomFields(
	input article.PublicationInput, faqItems []result.FAQItem, readingTime string, tagNames []string,
) []wordpress.CustomField {
	fields := make([]wordpress.CustomField, 0, 9+2*len(faqItems))
	fields = append(fields,
		wordpress.CustomField{Key: "blog_tldr", Value: strings.TrimSpace(input.TLDR)},
		wordpress.CustomField{Key: "blog_read", Value: readingTime},
		wordpress.CustomField{Key: "blog_faq", Value: strconv.Itoa(len(faqItems))},
	)
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

// readingTimeFromResult достаёт готовое время чтения из result.md.
//
// Значение не пересчитывается: в PostgreSQL его нет, а единственный расчёт живёт в сборке
// result.md. Брать его оттуда — значит опубликовать ровно то, что человек уже видел в файле.
//
// Каталог статьи выводится из пути к article.html, а не из slug: у пути к артефакту первый
// сегмент и есть каталог, и лишнего источника истины не появляется.
func readingTimeFromResult(writer wordPressPublishWriter, htmlPath string) (string, error) {
	directory := strings.Split(strings.Trim(strings.ReplaceAll(htmlPath, "\\", "/"), "/"), "/")[0]
	if directory == "" {
		return "", fmt.Errorf("не удалось определить каталог статьи по пути %q", htmlPath)
	}
	resultPath := directory + "/" + articleoutput.ResultFileName
	if !writer.Exists(resultPath) {
		return "", fmt.Errorf("нет файла %s — время чтения брать неоткуда, соберите result", resultPath)
	}
	content, err := writer.Read(resultPath)
	if err != nil {
		return "", fmt.Errorf("прочитать %s: %w", resultPath, err)
	}
	value := fencedSectionValue(content, readingTimeSection)
	if value == "" {
		return "", fmt.Errorf("в %s пуст раздел %q", resultPath, readingTimeSection)
	}
	return value, nil
}

// fencedSectionValue читает значение раздела result.md, обрамлённое ```text.
func fencedSectionValue(content, header string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != header {
			continue
		}
		var value []string
		inside := false
		for _, next := range lines[index+1:] {
			trimmed := strings.TrimSpace(next)
			if strings.HasPrefix(trimmed, "```") {
				if inside {
					return strings.TrimSpace(strings.Join(value, "\n"))
				}
				inside = true
				continue
			}
			// Следующий заголовок раньше открывающей ограды означает пустой раздел.
			if !inside && strings.HasPrefix(trimmed, "## ") {
				return ""
			}
			if inside {
				value = append(value, next)
			}
		}
		return strings.TrimSpace(strings.Join(value, "\n"))
	}
	return ""
}

// runWordPressPublishAll публикует все подходящие статьи.
//
// Выборка своя, а не ветка в GetPendingForOperation: там каждый предикат начинается с
// status <> 'completed', потому что описывает незавершённый пайплайн, а здесь условие ровно
// обратное.
//
// Останавливается на первой же ошибке. Отказ с неизвестным исходом требует человека, а не
// следующей статьи: продолжив, мы писали бы в блог, не разобравшись с предыдущей записью.
//
// publish приходит параметром, а не зовётся напрямую: бюджет времени принадлежит одной
// статье, и ставит его вызывающий — здесь он был бы общим на весь список.
func runWordPressPublishAll(
	ctx context.Context, deps wordPressPublishDeps, publish func(context.Context, string) error,
) error {
	externalIDs, err := deps.repository.ListPublishable(ctx)
	if err != nil {
		return err
	}
	if len(externalIDs) == 0 {
		fmt.Fprintln(deps.out, "Публиковать нечего: подходящих статей нет.")
		return nil
	}
	fmt.Fprintf(deps.out, "К публикации %d статей: %s\n", len(externalIDs), strings.Join(externalIDs, ", "))
	fmt.Fprintln(deps.out, "Публикация необратима: удалять записи приложение не умеет.")
	confirmed, err := confirmDestructive(
		wordPressPublishOperation, deps.assumeYes, deps.interactive, deps.in, deps.out)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(deps.out, "Публикация отменена.")
		deps.logger.Info("массовая публикация отменена", "stage", "confirm")
		return nil
	}

	published := 0
	for index, externalID := range externalIDs {
		fmt.Fprintf(deps.out, "\n[%d/%d] статья %s\n", index+1, len(externalIDs), externalID)
		if err := publish(ctx, externalID); err != nil {
			// Итог печатается и при остановке: человеку нужно знать, сколько записей уже
			// создано в блоге, — повторный запуск начнёт с той статьи, на которой встали.
			fmt.Fprintf(deps.out, "\nОпубликовано статей: %d из %d.\n", published, len(externalIDs))
			fmt.Fprintf(deps.out, "Остановлено на статье %s. Причина:\n  %v\n", externalID, err)
			if remaining := externalIDs[index+1:]; len(remaining) > 0 {
				fmt.Fprintf(deps.out, "Не тронуты: %s\n", strings.Join(remaining, ", "))
			}
			return fmt.Errorf("массовая публикация остановлена на статье %s: %w", externalID, err)
		}
		published++
	}
	fmt.Fprintf(deps.out, "\nОпубликовано статей: %d\n", published)
	return nil
}

// runWordPressMarkPublished привязывает статью к записи, существовавшей в блоге до нас.
//
// Состояние ставится linked, а не published: эту запись собирал человек, и что в ней лежит,
// приложение не знает. Ничего в WordPress не создаётся и не меняется — при указанном postID
// делается один читающий запрос, чтобы убедиться, что запись существует, и взять её адрес.
//
// Автоматически записи не ищутся: сопоставление по заголовку даёт ложные совпадения, а ошибка
// здесь стоит либо дубля в блоге, либо молча пропущенной статьи.
func runWordPressMarkPublished(
	ctx context.Context, deps wordPressPublishDeps, externalID string, postID int64,
) error {
	input, err := deps.repository.GetPublicationInput(ctx, externalID)
	if err != nil {
		return err
	}
	if input.Publication.InWordPress() {
		fmt.Fprintf(deps.out, "Статья %s уже помечена как присутствующая в WordPress (%s), ничего не изменилось.\n",
			externalID, input.Publication.Status)
		return nil
	}

	url := ""
	if postID > 0 {
		stored, getErr := deps.client.GetPost(ctx, postID)
		if getErr != nil {
			return fmt.Errorf("прочитать запись %d перед привязкой к статье %s: %w", postID, externalID, getErr)
		}
		if stored.ID != postID {
			return fmt.Errorf("запись %d не найдена в WordPress — привязывать статью %s не к чему", postID, externalID)
		}
		url = stored.Link
	}
	if err := deps.repository.LinkPublication(ctx, externalID, postID, url); err != nil {
		return err
	}

	deps.logger.Info("статья привязана к существующей записи WordPress",
		"external_id", externalID, "stage", "wordpress_mark_published",
		"post_id", postID, "url", url, "result", "ok")
	if postID > 0 {
		fmt.Fprintf(deps.out, "Статья %s привязана к записи %d: %s\n", externalID, postID, url)
		fmt.Fprintln(deps.out, "Состояние linked: запись создана вручную, не нашим publisher. В WordPress ничего не отправлялось.")
		return nil
	}
	fmt.Fprintf(deps.out, "Статья %s помечена как присутствующая в WordPress. В WordPress ничего не отправлялось.\n", externalID)
	fmt.Fprintln(deps.out, "Запись не указана, поэтому раздел со ссылкой в result.md останется пустым.")
	return nil
}

// wordPressCommandDeps — то, что composition root передаёт обеим командам публикации.
//
// Отдельный тип и отдельная функция, потому что main() и без того за порогом сложности:
// разбор публикации в её теле добавил бы ещё одну ветвящуюся страницу к тому, что уже
// перевалило за все ориентиры.
type wordPressCommandDeps struct {
	repository  wordPressPublishRepository
	writer      wordPressPublishWriter
	images      wordPressImageSource
	resultBuild wordPressResultBuilder
	settings    config.WordPressConfig
	logger      *slog.Logger
	out         io.Writer
	in          io.Reader
	assumeYes   bool
	interactive bool
}

// wordPressPublishDepsFrom собирает зависимости публикации из того, что дал composition root.
//
// Общая точка сборки у команды publish и у финального этапа полного прогона: разные наборы
// зависимостей у одного и того же действия означали бы, что прогон публикует не так, как
// команда, и разошлись бы они молча.
func wordPressPublishDepsFrom(deps wordPressCommandDeps, client wordPressPublishClient) wordPressPublishDeps {
	return wordPressPublishDeps{
		client:      client,
		repository:  deps.repository,
		writer:      deps.writer,
		images:      deps.images,
		resultBuild: deps.resultBuild,
		logger:      deps.logger,
		out:         deps.out,
		in:          deps.in,
		assumeYes:   deps.assumeYes,
		interactive: deps.interactive,
	}
}

// newWordPressRunPublish готовит публикацию одной статьи для полного прогона.
//
// Клиент создаётся один на весь прогон, а бюджет времени ставится на статью: у прогона он
// свой на каждую, иначе последние статьи публиковались бы с уже истёкшим временем.
//
// Ошибка здесь означает, что публиковать нечем (нет адреса площадки или credentials). Она
// возвращается наружу, а не глотается: решать, продолжать ли прогон без публикации, —
// дело вызывающего, но узнать об этом он обязан до первой статьи, а не после последней.
func newWordPressRunPublish(deps wordPressCommandDeps) (func(context.Context, string) error, error) {
	client, err := newWordPressClient(deps.settings)
	if err != nil {
		return nil, err
	}
	publishDeps := wordPressPublishDepsFrom(deps, client)
	return func(ctx context.Context, externalID string) error {
		ctx, cancel := context.WithTimeout(ctx, wordPressPublishDeadline)
		defer cancel()
		return runWordPressPublish(ctx, publishDeps, externalID)
	}, nil
}

// runWordPressCommand разводит publish и mark-published.
//
// Клиент создаётся только там, где он нужен: mark-published в WordPress не ходит вовсе, и
// требовать от неё рабочих credentials было бы враньём о том, что команда делает.
func runWordPressCommand(
	ctx context.Context, deps wordPressCommandDeps, operation, externalID string, postID int64, plan bool,
) error {
	publishDeps := wordPressPublishDepsFrom(deps, nil)
	// Клиент создаётся только там, где он нужен. mark-published без указанной записи в
	// WordPress не ходит вовсе, и требовать от неё рабочих credentials было бы враньём о том,
	// что команда делает.
	if operation == wordPressMarkPublishedOperation && postID == 0 {
		return runWordPressMarkPublished(ctx, publishDeps, externalID, 0)
	}

	client, err := newWordPressClient(deps.settings)
	if err != nil {
		return err
	}
	publishDeps.client = client

	if operation == wordPressMarkPublishedOperation {
		ctx, cancel := context.WithTimeout(ctx, wordPressRequestTimeout*3)
		defer cancel()
		return runWordPressMarkPublished(ctx, publishDeps, externalID, postID)
	}

	// Бюджет ставится на статью и ровно один раз: у массового прогона он свой на каждую,
	// а не общий на весь список — иначе последние статьи публиковались бы с уже истёкшим
	// временем.
	publish := func(ctx context.Context, externalID string) error {
		ctx, cancel := context.WithTimeout(ctx, wordPressPublishDeadline)
		defer cancel()
		return runWordPressPublish(ctx, publishDeps, externalID)
	}
	// Сухой прогон разбирается после создания клиента: справочники рубрик и меток он читает,
	// а вот записывать не будет ничего.
	if plan {
		if externalID != "" {
			ctx, cancel := context.WithTimeout(ctx, wordPressPublishDeadline)
			defer cancel()
			return runWordPressPublishPlan(ctx, publishDeps, externalID)
		}
		return runWordPressPublishPlanAll(ctx, publishDeps)
	}

	if externalID != "" {
		return publish(ctx, externalID)
	}
	return runWordPressPublishAll(ctx, publishDeps, publish)
}

// fieldValuePreview — сколько символов значения показывать в сухом прогоне.
//
// Целиком печатать нельзя: TL;DR и ответы FAQ — это абзацы, и отчёт по одной статье занял бы
// экран. Обрезанного хватает, чтобы увидеть, то ли значение подставилось.
const fieldValuePreview = 72

// runWordPressPublishPlan показывает, что уйдёт в WordPress, ничего не отправляя.
//
// Единственные запросы — чтение справочников рубрик и меток: без них имена не превратятся в
// идентификаторы, а именно это чаще всего и ломает публикацию. Записывающих запросов нет ни
// одного, состояние статьи не меняется.
//
// Смысл команды в необратимости publish: запись создаётся одним вызовом, отменить её нельзя,
// и единственная возможность посмотреть на нагрузку — посмотреть до отправки.
func runWordPressPublishPlan(ctx context.Context, deps wordPressPublishDeps, externalID string) error {
	payload, plan, err := buildWordPressPayload(ctx, deps, externalID, false)
	if err != nil {
		fmt.Fprintf(deps.out, "Статья %s к публикации НЕ готова:\n  %v\n", externalID, err)
		return err
	}

	out := deps.out
	fmt.Fprintf(out, "Статья %s — сухой прогон. В WordPress ничего не отправлено.\n\n", externalID)

	fmt.Fprintln(out, "Запись:")
	fmt.Fprintf(out, "  %-22s %s\n", "post_title", payload.Title)
	fmt.Fprintf(out, "  %-22s %s\n", "post_status", payload.Status)
	fmt.Fprintf(out, "  %-22s %d символов из %s\n", "post_content", plan.ContentRunes, plan.HTMLPath)
	fmt.Fprintf(out, "  %-22s %d  «%s»\n", "terms.category", payload.CategoryID, plan.CategoryName)
	tags := make([]string, 0, len(plan.Tags))
	var newTags int
	for _, tag := range plan.Tags {
		if tag.ID == 0 {
			newTags++
			tags = append(tags, fmt.Sprintf("новая «%s»", tag.Name))
			continue
		}
		tags = append(tags, fmt.Sprintf("%d «%s»", tag.ID, tag.Name))
	}
	fmt.Fprintf(out, "  %-22s %s\n", "terms.post_tag", strings.Join(tags, ", "))
	// Обложка показывается путём и именем в библиотеке: имя видно в адресе картинки на
	// сайте, а путь — единственный способ убедиться, что уйдёт та самая картинка.
	fmt.Fprintf(out, "  %-22s %s → %s, %s (%s)\n", "post_thumbnail",
		plan.Image.Path, plan.Image.MediaName, formatMegabytes(plan.Image.Size), plan.Image.MIMEType)
	// Подписи показываются целиком: это то, что увидит читатель и поисковик, и обрезка
	// здесь скрыла бы ровно ту разницу, ради которой сухой прогон и запускают.
	fmt.Fprintf(out, "  %-22s %s\n", "media.alt_text", plan.ImageAlt)
	fmt.Fprintf(out, "  %-22s %s\n", "media.title", plan.ImageTitle)

	fmt.Fprintf(out, "\ncustom_fields (%d):\n", len(payload.Fields))
	for _, field := range payload.Fields {
		value := strings.ReplaceAll(strings.TrimSpace(field.Value), "\n", " ")
		fmt.Fprintf(out, "  %-22s = %s\n", field.Key, truncateForPlan(value, fieldValuePreview))
	}

	fmt.Fprintf(out, "\nВремя чтения %s взято из result.md, FAQ разобран в %d пар.\n",
		plan.ReadingTime, plan.FAQItems)
	fmt.Fprintln(out, "Обложка при публикации уйдёт в медиабиблиотеку отдельным вызовом вместе с alt и title —")
	fmt.Fprintln(out, "сейчас она не отправлена. alt взят из заголовка H1 статьи, title — из image_slug.")
	if newTags > 0 {
		// Заведение метки необратимо ровно так же, как запись: удалять термины приложение
		// не умеет. Сухой прогон для того и нужен, чтобы опечатку в Excel заметили здесь.
		fmt.Fprintf(out, "Меток на площадке ещё нет: %d — publish заведёт их перед созданием записи.\n", newTags)
		fmt.Fprintln(out, "Проверьте написание: удалять метки приложение не умеет.")
	}
	fmt.Fprintln(out, "Статья готова к публикации.")
	deps.logger.Info("сухой прогон публикации", "external_id", externalID,
		"stage", "wordpress_publish_plan", "fields", len(payload.Fields), "result", "ok")
	return nil
}

// runWordPressPublishPlanAll прогоняет проверку по всем статьям, ждущим публикации.
//
// Отчёт по каждой не печатается: смысл массового прогона в том, чтобы до необратимой команды
// узнать, какие статьи отвалятся и почему. Подробности по одной смотрят её собственным планом.
func runWordPressPublishPlanAll(ctx context.Context, deps wordPressPublishDeps) error {
	externalIDs, err := deps.repository.ListPublishable(ctx)
	if err != nil {
		return err
	}
	if len(externalIDs) == 0 {
		fmt.Fprintln(deps.out, "Публиковать нечего: подходящих статей нет.")
		return nil
	}
	fmt.Fprintf(deps.out, "Проверка %d статей. В WordPress ничего не отправляется.\n\n", len(externalIDs))

	var failed []string
	for _, externalID := range externalIDs {
		_, plan, buildErr := buildWordPressPayload(ctx, deps, externalID, false)
		if buildErr != nil {
			failed = append(failed, externalID)
			fmt.Fprintf(deps.out, "  %-4s НЕ ГОТОВА  %v\n", externalID, buildErr)
			continue
		}
		fmt.Fprintf(deps.out, "  %-4s готова     рубрика «%s», меток %d (новых %d), FAQ %d, чтение %s, обложка %s\n",
			externalID, plan.CategoryName, len(plan.Tags), countNewWordPressTags(plan.Tags),
			plan.FAQItems, plan.ReadingTime, formatMegabytes(plan.Image.Size))
	}

	fmt.Fprintf(deps.out, "\nГотовы к публикации: %d из %d.\n", len(externalIDs)-len(failed), len(externalIDs))
	if len(failed) > 0 {
		// Ненулевой код намеренно: сухой прогон нужен как проверка перед необратимой
		// командой, и «часть статей отвалится» — это не успех.
		return fmt.Errorf("не готовы к публикации: %s", strings.Join(failed, ", "))
	}
	return nil
}

// countNewWordPressTags считает метки, которых на площадке ещё нет.
func countNewWordPressTags(tags []wordpress.Tag) int {
	var count int
	for _, tag := range tags {
		if tag.ID == 0 {
			count++
		}
	}
	return count
}

// formatMegabytes показывает вес файла так, как о нём думает человек.
func formatMegabytes(size int64) string {
	return fmt.Sprintf("%.1f МБ", float64(size)/(1<<20))
}

func truncateForPlan(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

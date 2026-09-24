package main

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
)

// wordPressRepublishOperation переписывает тело уже опубликованной статьи.
//
// Отдельная операция, а не флаг публикации: `publish` создаёт запись и от повтора защищён
// отметкой в базе, а эта команда работает только по существующей записи и создать ничего не
// может. Нужна она после того, как статью перегенерировали — текст в блоге иначе остаётся
// прежним навсегда, потому что переписывать записи publisher не умеет и не должен.
//
// Меняются заголовок, тело и связь со связанными курсами (republishFields). Слаг, дата,
// рубрика, метки, обложка и остальные поля ACF не называются вовсе — не отправленное поле
// WordPress не трогает, и накопленные позиции вместе с адресом остаются на месте.
const wordPressRepublishOperation = "republish"

// wordPressRepublishClient — то, что операция требует от площадки. Создание записи сюда не
// входит намеренно: команда, умеющая только править, не создаст дубль ни при какой ошибке.
type wordPressRepublishClient interface {
	GetPost(ctx context.Context, postID int64) (wordpress.StoredPost, error)
	EditPost(ctx context.Context, update wordpress.PostUpdate) error
}

// republishFields — поля записи, которые правка обновляет вместе с текстом.
//
// Список, а не «все поля нагрузки»: правка узкая по замыслу, и переписывать ею рубрику,
// метки или обложку незачем — их человек мог поправить в админке, и наша копия была бы
// старее. Сюда попадает только то, что пересобирается вместе с самим текстом и описывает
// именно его:
//
//   - связанные курсы — подбираются из каталога вместе с текстом, и перегенерированная
//     статья обязана унести под собой те карточки, что напечатаны в её result.md;
//   - краткое содержание и время чтения — их пишет стадия info по готовому тексту, и после
//     перегенерации прежние значения описывают статью, которой больше нет. Измерено на
//     статьях 86 и 87: тело в блоге переписали, а над ним остался блок «Кратко о статье»
//     про другую профессию.
//
// У записей, созданных раньше самих полей, их нет вовсе — такое поле правка заводит впервые.
var republishFields = []string{blogFieldRelatedCourses, blogFieldTLDR, blogFieldReadTime}

// bodyImageRE — картинка, которую публикация вставила в тело записи. Из неё берутся адрес
// вложения и его идентификатор: грузить файл второй раз значит плодить копии в
// медиабиблиотеке, а больше этот адрес нигде не хранится.
//
// Ищется только сам <img>: обёртка и подпись вокруг него у площадок разные и собираются
// заново, а не переносятся.
var bodyImageRE = regexp.MustCompile(`(?is)<img[^>]*class="[^"]*wp-image-(\d+)[^"]*"[^>]*>`)

// bodyImageSrcRE — адрес файла внутри найденного <img>.
var bodyImageSrcRE = regexp.MustCompile(`(?is)\bsrc="([^"]+)"`)

// storedBodyImage вынимает из прежней записи вложение, которым была её картинка тела.
func storedBodyImage(contentHTML string) (wordpress.UploadedMedia, bool) {
	tag := bodyImageRE.FindStringSubmatch(contentHTML)
	if len(tag) < 2 {
		return wordpress.UploadedMedia{}, false
	}
	id, err := strconv.ParseInt(tag[1], 10, 64)
	if err != nil {
		return wordpress.UploadedMedia{}, false
	}
	src := bodyImageSrcRE.FindStringSubmatch(tag[0])
	if len(src) < 2 {
		return wordpress.UploadedMedia{}, false
	}
	return wordpress.UploadedMedia{AttachmentID: id, URL: html.UnescapeString(src[1])}, true
}

func runWordPressRepublish(
	ctx context.Context, deps wordPressPublishDeps, client wordPressRepublishClient, externalID string,
) error {
	started := time.Now()
	logger := deps.logger.With("external_id", externalID, "stage", "wordpress_republish")

	input, err := deps.repository.GetPublicationInput(ctx, externalID)
	if err != nil {
		return err
	}
	if !input.Publication.InWordPress() || input.Publication.PostID == nil {
		return fmt.Errorf("статья %s в блоге не опубликована — переписывать нечего: "+
			"её выкладывает publish", externalID)
	}
	postID := *input.Publication.PostID

	// Нагрузка собирается тем же сборщиком, что и у публикации, но метки не заводятся: правка
	// их не отправляет, и создавать термины ради неотправленного поля незачем.
	payload, plan, err := buildWordPressPayloadFor(ctx, deps, externalID, false, repository.ValidateRepublishInput)
	if err != nil {
		return err
	}

	stored, err := client.GetPost(ctx, postID)
	if err != nil {
		return fmt.Errorf("прочитать запись %d статьи %s: %w", postID, externalID, err)
	}
	// Картинка тела берётся из прежней записи: она пришла из медиабиблиотеки при первой
	// публикации, и её адрес больше нигде не хранится. А вот блок вокруг неё собирается
	// заново, теми же правилами, что и при публикации.
	//
	// Переносить готовый кусок разметки нельзя, и это измерено: у страницы услуги картинка
	// обёрнута <figure>, подпись идёт отдельным абзацем, а место — перед первым заголовком, а
	// не в середине. Перенос куска ронял обёртку с подписью и уводил картинку в середину —
	// каждая правка молча портила запись ещё на шаг.
	if media, ok := storedBodyImage(stored.ContentHTML); ok {
		payload.ContentHTML = insertBodyImage(plan, media, payload.ContentHTML)
		logger.Info("картинка тела пересобрана из прежней записи", "post_id", postID,
			"attachment_id", media.AttachmentID)
	}

	fields := republishFieldUpdates(payload, stored)
	logger.Info("перезапись начата", "post_id", postID,
		"old_chars", len([]rune(stored.ContentHTML)), "new_chars", len([]rune(payload.ContentHTML)),
		"fields", len(fields))

	if err := client.EditPost(ctx, wordpress.PostUpdate{
		PostID:      postID,
		Title:       payload.Title,
		ContentHTML: payload.ContentHTML,
		Fields:      fields,
	}); err != nil {
		logger.Error("перезапись не удалась", "post_id", postID, "error", err,
			"duration_ms", time.Since(started).Milliseconds())
		return fmt.Errorf("переписать запись %d статьи %s: %w", postID, externalID, err)
	}

	// Сверка после записи: та же причина, что и у публикации, — площадка вправе принять
	// запрос и сохранить не всё.
	after, err := client.GetPost(ctx, postID)
	if err != nil {
		logger.Warn("запись переписана, но прочитать её обратно не удалось",
			"post_id", postID, "error", err)
	} else if !strings.Contains(plainText(after.ContentHTML), firstTextFragment(payload.ContentHTML)) {
		logger.Error("запись переписана, но текст в блоге не совпал с отправленным",
			"post_id", postID, "stored_chars", len([]rune(after.ContentHTML)))
		return fmt.Errorf("запись %d переписана, но её текст не совпал с отправленным: "+
			"проверьте статью %s в админке", postID, externalID)
	} else if mismatches := wordpress.VerifyFields(fields, after); len(mismatches) > 0 {
		// Отказ, а не предупреждение: команду позвали ради содержимого записи целиком, и
		// молча отброшенное поле человек увидел бы только глазами в админке. Текст при этом
		// уже лёг — об этом и говорит сообщение, чтобы правку не гоняли второй раз зря.
		var report strings.Builder
		for _, mismatch := range mismatches {
			fmt.Fprintf(&report, "\n  - %s", mismatch)
		}
		logger.Error("запись переписана, но поля сохранились не все",
			"post_id", postID, "mismatches", len(mismatches))
		return fmt.Errorf("запись %d переписана, текст лёг, но сохранились не все поля:%s\n"+
			"Проверьте статью %s в админке", postID, report.String(), externalID)
	}

	logger.Info("перезапись завершена", "post_id", postID, "url", stored.Link,
		"result", "ok", "duration_ms", time.Since(started).Milliseconds())
	fmt.Fprintf(deps.out, "Тело статьи %s переписано: %s (запись %d)\n", externalID, stored.Link, postID)
	// Курсы печатаются поимённо: это то же, что напечатано в result.md, и человек смотрит
	// сюда затем, чтобы убедиться — под статьёй встали именно они.
	for _, course := range plan.RelatedCourses {
		fmt.Fprintf(deps.out, "  курс: %s\n", course.Program.Name)
	}

	if _, buildErr := deps.resultBuild.Build(ctx, externalID); buildErr != nil {
		logger.Warn("result.md не пересобран", "error", buildErr)
	}
	return nil
}

// republishFieldUpdates отбирает из нагрузки поля, которые уходят правкой.
//
// Идентификатор postmeta берётся у прочитанной записи: с ним wp.editPost обновит
// существующее поле, без него — заведёт впервые. Отсутствие ключа у записи поэтому не
// ошибка, а обычное состояние статьи, опубликованной раньше самого блока курсов.
//
// Поля, которого нет в нагрузке, в правке не окажется: у задачи без каталога связь не
// подбирается вовсе, и пустое поле означало бы «снять курсы», а не «оставить как есть».
func republishFieldUpdates(
	payload wordpress.PostPayload, stored wordpress.StoredPost,
) []wordpress.FieldUpdate {
	updates := make([]wordpress.FieldUpdate, 0, len(republishFields))
	for _, key := range republishFields {
		for _, field := range payload.Fields {
			if field.Key != key {
				continue
			}
			id := stored.FieldIDs[key]
			updates = append(updates, wordpress.FieldUpdate{
				ID: id, Key: field.Key, Value: field.Value, IDs: field.IDs, Create: id == "",
			})
		}
	}
	return updates
}

// firstTextFragment возвращает кусок текста из начала разметки — по нему сверяется, что в блог
// легло именно отправленное. Сравнивать разметку целиком нельзя: площадка нормализует пробелы
// и переносы, дописывает свои обёртки, и полное равенство не сходится никогда. Обе стороны
// сравнения обязаны пройти одну и ту же нормализацию — иначе сверка врёт о полностью
// записанной статье.
func firstTextFragment(markup string) string {
	runes := []rune(plainText(markup))
	if len(runes) > 60 {
		runes = runes[:60]
	}
	return string(runes)
}

// plainText оставляет от разметки только её текст со схлопнутыми пробелами.
func plainText(markup string) string {
	return strings.Join(strings.Fields(stripHTMLTags(markup)), " ")
}

func stripHTMLTags(markup string) string { return anyHTMLTagRE.ReplaceAllString(markup, " ") }

var anyHTMLTagRE = regexp.MustCompile(`(?s)<[^>]+>`)

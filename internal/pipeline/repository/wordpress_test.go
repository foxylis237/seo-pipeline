package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

func publishableInput() article.PublicationInput {
	return article.PublicationInput{
		Article: article.Article{
			ID: 1, ExternalID: "16", Title: "Разряды газосварщиков", Status: "completed",
			// Слаг картинки уходит подписью вложения в медиабиблиотеке.
			Slug: "razryady-gazosvarshchikov",
		},
		Publication:     article.Publication{Status: article.WordPressNotPublished},
		Category:        "Сварка, слесарка и металлообработка",
		Tags:            "Газосварщик, Повышение квалификации",
		Keyword:         "разряды газосварщиков",
		MetaDescription: "Какие категории существуют.",
		Header:          "Разряды газосварщиков: какие бывают",
		TLDR:            "От разряда зависит сложность работ.",
		FAQ:             "Вопрос: Сколько разрядов?\nОтвет: Пять.",
		HTMLPath:        "16-razryady-gazosvarshchikov/article.html",
	}
}

func TestValidatePublicationInputAcceptsReadyArticle(t *testing.T) {
	if err := ValidatePublicationInput(publishableInput()); err != nil {
		t.Fatalf("готовая статья отбита: %v", err)
	}
}

func TestValidatePublicationInputRejects(t *testing.T) {
	published := int64(21593)
	cases := map[string]struct {
		mutate func(*article.PublicationInput)
		want   string
	}{
		// Статуса completed мало: в базе живёт статья со статусом completed, у которой нет
		// ни строки article_outputs, ни article_metadata. Ровно поэтому проверяются данные,
		// а не только статус.
		"не completed": {
			func(i *article.PublicationInput) { i.Article.Status = "processing" }, "не прошла пайплайн"},
		"висит ошибка": {
			func(i *article.PublicationInput) {
				message := "Arsenkin не ответил"
				i.Article.ErrorMessage = &message
			}, "висит ошибка"},
		"уже опубликована нашим publisher": {
			func(i *article.PublicationInput) {
				i.Publication = article.Publication{Status: article.WordPressPublished, PostID: &published}
			}, "уже есть в WordPress"},
		// Дубль одинаково недопустим и для привязанной вручную записи.
		"привязана вручную": {
			func(i *article.PublicationInput) {
				i.Publication = article.Publication{Status: article.WordPressLinked, PostID: &published}
			}, "уже есть в WordPress"},
		"нет HTML":         {func(i *article.PublicationInput) { i.HTMLPath = "" }, "HTML статьи"},
		"нет рубрики":      {func(i *article.PublicationInput) { i.Category = "  " }, "рубрика"},
		"нет меток":        {func(i *article.PublicationInput) { i.Tags = "" }, "метки"},
		"нет ключа":        {func(i *article.PublicationInput) { i.Keyword = "" }, "фокусное ключевое слово"},
		"нет мета":         {func(i *article.PublicationInput) { i.MetaDescription = "" }, "мета-описание"},
		"нет заголовка H1": {func(i *article.PublicationInput) { i.Header = "" }, "заголовок профблока"},
		"нет TL;DR":        {func(i *article.PublicationInput) { i.TLDR = "" }, "TL;DR"},
		"нет FAQ":          {func(i *article.PublicationInput) { i.FAQ = "" }, "FAQ"},
		// Без слага картинке неоткуда взять подпись в медиабиблиотеке, а исправить
		// вложение приложение не умеет.
		"нет image_slug": {func(i *article.PublicationInput) { i.Article.Slug = "" }, "image_slug"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			input := publishableInput()
			testCase.mutate(&input)
			err := ValidatePublicationInput(input)
			if err == nil {
				t.Fatal("ожидалась ошибка")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("ошибка %q не называет причину %q", err, testCase.want)
			}
		})
	}
}

func TestValidatePublicationInputNamesPostInAlreadyPublished(t *testing.T) {
	input := publishableInput()
	postID := int64(21593)
	input.Publication = article.Publication{Status: article.WordPressPublished, PostID: &postID}
	err := ValidatePublicationInput(input)
	if err == nil || !strings.Contains(err.Error(), "21593") {
		t.Fatalf("ошибка не называет запись: %v", err)
	}
}

func TestPublicationDefaultsToNotPublished(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()

	created, err := repository.Create(ctx, article.Input{ExcelID: 16, Title: "Статья", ImageSlug: "slug"})
	if err != nil {
		t.Fatal(err)
	}
	input, err := repository.GetPublicationInput(ctx, created.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	// Значение приходит из умолчания колонки, а не из кода: NULL как состояние не используется.
	if input.Publication.Status != article.WordPressNotPublished {
		t.Fatalf("статус публикации новой статьи = %q, ожидался %q",
			input.Publication.Status, article.WordPressNotPublished)
	}
	if input.Publication.InWordPress() {
		t.Fatal("новая статья считается опубликованной")
	}
	if input.Publication.PostID != nil || input.Publication.URL != "" {
		t.Fatalf("у новой статьи заполнены post_id или url: %+v", input.Publication)
	}
}

func TestSavePublicationStoresPostAndURL(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()

	created, err := repository.Create(ctx, article.Input{ExcelID: 16, Title: "Статья", ImageSlug: "slug"})
	if err != nil {
		t.Fatal(err)
	}
	// Первый вызов идёт сразу после wp.newPost, когда адрес записи ещё неизвестен.
	if err := repository.SavePublication(ctx, created.ExternalID, 21602, ""); err != nil {
		t.Fatal(err)
	}
	input, err := repository.GetPublicationInput(ctx, created.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if !input.Publication.InWordPress() {
		t.Fatalf("статус после публикации = %q", input.Publication.Status)
	}
	if input.Publication.PostID == nil || *input.Publication.PostID != 21602 {
		t.Fatalf("post_id = %v", input.Publication.PostID)
	}
	if input.Publication.URL != "" {
		t.Fatalf("url без адреса = %q, ожидалась пустая строка", input.Publication.URL)
	}

	// Второй вызов дописывает адрес после успешной сверки.
	if err := repository.SavePublication(ctx, created.ExternalID, 21602, "https://example.test/blog/post/"); err != nil {
		t.Fatal(err)
	}
	input, err = repository.GetPublicationInput(ctx, created.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Publication.URL != "https://example.test/blog/post/" {
		t.Fatalf("url = %q", input.Publication.URL)
	}
}

func TestSavePublicationRejectsUnknownArticleAndPost(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()

	if err := repository.SavePublication(ctx, "404", 21602, ""); err == nil {
		t.Fatal("сохранение публикации несуществующей статьи прошло")
	}
	created, err := repository.Create(ctx, article.Input{ExcelID: 16, Title: "Статья", ImageSlug: "slug"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SavePublication(ctx, created.ExternalID, 0, ""); err == nil {
		t.Fatal("сохранение публикации без идентификатора записи прошло")
	}
}

func TestLinkPublicationWithoutPostLeavesRecordUnknown(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()

	created, err := repository.Create(ctx, article.Input{ExcelID: 16, Title: "Статья", ImageSlug: "slug"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.LinkPublication(ctx, created.ExternalID, 0, ""); err != nil {
		t.Fatal(err)
	}
	input, err := repository.GetPublicationInput(ctx, created.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	// Состояние linked, а не published: эту запись собирал человек, и наш publisher к ней
	// отношения не имеет.
	if input.Publication.Status != article.WordPressLinked {
		t.Fatalf("статус после привязки = %q, ожидался %q", input.Publication.Status, article.WordPressLinked)
	}
	if !input.Publication.InWordPress() {
		t.Fatal("привязанная статья не считается присутствующей в WordPress")
	}
	if input.Publication.CreatedByPipeline() {
		t.Fatal("привязанная вручную запись выдана за созданную нами")
	}
	// Какая именно запись соответствует статье, без второго аргумента не выясняется.
	if input.Publication.PostID != nil || input.Publication.URL != "" {
		t.Fatalf("привязка придумала запись: %+v", input.Publication)
	}
	// Повторная привязка не ошибка.
	if err := repository.LinkPublication(ctx, created.ExternalID, 0, ""); err != nil {
		t.Fatalf("повторная привязка: %v", err)
	}
	if err := repository.LinkPublication(ctx, "404", 0, ""); err == nil {
		t.Fatal("привязка несуществующей статьи прошла")
	}
}

func TestLinkPublicationStoresKnownPost(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()

	created, err := repository.Create(ctx, article.Input{ExcelID: 16, Title: "Статья", ImageSlug: "slug"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.LinkPublication(ctx, created.ExternalID, 21593, "https://example.test/blog/post/"); err != nil {
		t.Fatal(err)
	}
	input, err := repository.GetPublicationInput(ctx, created.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Publication.Status != article.WordPressLinked {
		t.Fatalf("статус = %q", input.Publication.Status)
	}
	if input.Publication.PostID == nil || *input.Publication.PostID != 21593 {
		t.Fatalf("post_id = %v", input.Publication.PostID)
	}
	if input.Publication.URL != "https://example.test/blog/post/" {
		t.Fatalf("url = %q", input.Publication.URL)
	}
	// Повторная привязка без записи не должна стирать уже известные идентификатор и адрес.
	if err := repository.LinkPublication(ctx, created.ExternalID, 0, ""); err != nil {
		t.Fatal(err)
	}
	input, err = repository.GetPublicationInput(ctx, created.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Publication.PostID == nil || *input.Publication.PostID != 21593 {
		t.Fatalf("повторная привязка стёрла post_id: %v", input.Publication.PostID)
	}
	if input.Publication.URL == "" {
		t.Fatal("повторная привязка стёрла адрес записи")
	}
}

func TestListPublishableSelectsOnlyCompletedAndNotPublished(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()

	completed, err := repository.Create(ctx, article.Input{ExcelID: 16, Title: "Готовая", ImageSlug: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	published, err := repository.Create(ctx, article.Input{ExcelID: 17, Title: "Опубликованная", ImageSlug: "s2"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := repository.Create(ctx, article.Input{ExcelID: 18, Title: "Незавершённая", ImageSlug: "s3"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{completed.ID, published.ID} {
		if err := repository.CompleteGeneration(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.SavePublication(ctx, published.ExternalID, 21602, ""); err != nil {
		t.Fatal(err)
	}

	publishable, err := repository.ListPublishable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(publishable) != 1 || publishable[0] != completed.ExternalID {
		t.Fatalf("к публикации выбрано %v, ожидалась только %s", publishable, completed.ExternalID)
	}
	_ = pending
}

// TestPublicationSurvivesArtifactResets — главный тест этой пары колонок.
//
// Отметка о публикации живёт на articles именно потому, что строку article_outputs удаляют
// целиком четыре пути, и один из них — штатный повторный prepare через SavePreparedResearch.
// Если отметка переживёт их все, дубль в блоге невозможен; если нет — следующий publish
// создаст второй пост, а удалять записи приложение не умеет.
func TestPublicationSurvivesArtifactResets(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()

	created, err := repository.Create(ctx, article.Input{ExcelID: 16, Title: "Статья", ImageSlug: "slug"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SavePublication(ctx, created.ExternalID, 21602, "https://example.test/blog/post/"); err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		name string
		run  func() error
	}{
		{"повторный prepare", func() error {
			return repository.SavePreparedResearch(ctx, created.ID,
				[]string{"запрос"}, []article.KeywordFrequency{{Query: "запрос", Frequency: 10}},
				[]string{"слово"}, "H1")
		}},
		{"regenerate", func() error { return repository.ResetGenerationState(ctx, created.ID) }},
		{"clear", func() error { return repository.ClearArticleState(ctx, created.ID) }},
		{"reset одной статьи", func() error { return repository.ResetArticleState(ctx, created.ID) }},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		input, err := repository.GetPublicationInput(ctx, created.ExternalID)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if !input.Publication.InWordPress() {
			t.Fatalf("%s стёр отметку о публикации — следующий publish создал бы дубль", step.name)
		}
		if input.Publication.PostID == nil || *input.Publication.PostID != 21602 {
			t.Fatalf("%s потерял post_id: %v", step.name, input.Publication.PostID)
		}
	}
}

// TestWordPressStatusConstraintRejectsUnknownValue проверяет, что состояние нельзя завести
// мимо двух разрешённых: маркер дубля сравнивается точно, и 'Published' от 'published'
// должно отличаться на уровне базы, а не на уровне внимательности.
func TestWordPressStatusConstraintRejectsUnknownValue(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()

	created, err := repository.Create(ctx, article.Input{ExcelID: 16, Title: "Статья", ImageSlug: "slug"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"Published", "publish", "Linked", "", "unknown"} {
		_, err := pool.Exec(ctx, "UPDATE articles SET wordpress_status = $2 WHERE id = $1", created.ID, value)
		if err == nil {
			t.Fatalf("база приняла недопустимый статус публикации %q", value)
		}
	}
	if _, err := pool.Exec(ctx,
		"UPDATE articles SET wordpress_status = NULL WHERE id = $1", created.ID); err == nil {
		t.Fatal("база приняла NULL как состояние публикации")
	}
}

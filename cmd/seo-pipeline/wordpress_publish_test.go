package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

const (
	testWPHTMLPath    = "16-razryady-gazosvarshchikov/article.html"
	testWPResultPath  = "16-razryady-gazosvarshchikov/result.md"
	testWPArticleHTML = `<p class="ds-markdown-paragraph">Текст статьи</p>`
	testWPResultMD    = "## Заголовок (H1)\n\n```text\nРазряды газосварщиков: какие бывают\n```\n\n" +
		"## Время чтения\n\n```text\n9 мин\n```\n\n## HTML\n\n```text\n" + testWPHTMLPath + "\n```\n"
)

// --- дублёры ---

type fakeWPRepository struct {
	input       article.PublicationInput
	loadErr     error
	publishable []string
	listErr     error
	saveErr     error
	markErr     error
	saved       []savedWPPublication
	linked      []savedWPPublication
}

type savedWPPublication struct {
	externalID string
	postID     int64
	url        string
}

func (r *fakeWPRepository) GetPublicationInput(context.Context, string) (article.PublicationInput, error) {
	return r.input, r.loadErr
}

func (r *fakeWPRepository) ListPublishable(context.Context) ([]string, error) {
	return r.publishable, r.listErr
}

func (r *fakeWPRepository) SavePublication(_ context.Context, externalID string, postID int64, url string) error {
	r.saved = append(r.saved, savedWPPublication{externalID, postID, url})
	return r.saveErr
}

func (r *fakeWPRepository) LinkPublication(_ context.Context, externalID string, postID int64, url string) error {
	r.linked = append(r.linked, savedWPPublication{externalID, postID, url})
	return r.markErr
}

type fakeWPClient struct {
	categoryID  int64
	categoryErr error
	tagIDs      map[string]int64
	tagErr      error
	// createdTags — метки, заведённые публикацией, в порядке заведения. Заведение видно
	// только здесь: в нагрузке метки лежат идентификаторами, и по ним не отличить старую
	// от новой.
	createdTags  []string
	createTagErr error
	nextTagID    int64
	attachmentID int64
	uploadErr    error
	uploaded     []wordpress.MediaFile
	createErr    error
	created      []wordpress.PostPayload
	// uploadsAtCreate — сколько файлов было загружено к моменту создания записи. Порядок
	// здесь и есть предмет проверки: назначить обложку после создания записи нельзя.
	uploadsAtCreate int
	stored          wordpress.StoredPost
	getErr          error
	// Поля страницы услуги: рубрика своей таксономии и связь с записью преподавателя.
	termLookups   []string
	postLookups   []string
	teacherID     int64
	teacherErr    error
	dropField     string
	dropThumbnail bool
}

func (c *fakeWPClient) FindCategoryID(context.Context, string) (int64, error) {
	return c.categoryID, c.categoryErr
}

// FindTermIDInTaxonomy обслуживает рубрику страницы услуги: та живёт в своей таксономии, а
// не во встроенной category. Ответ тот же самый — раскладка задачи выбирает вызов, а не
// поведение площадки.
func (c *fakeWPClient) FindTermIDInTaxonomy(_ context.Context, taxonomy, name string) (int64, error) {
	c.termLookups = append(c.termLookups, taxonomy+"/"+name)
	return c.categoryID, c.categoryErr
}

// FindPostIDByTitle обслуживает связь с преподавателем: ACF хранит идентификатор записи.
func (c *fakeWPClient) FindPostIDByTitle(_ context.Context, postType, title string) (int64, error) {
	c.postLookups = append(c.postLookups, postType+"/"+title)
	if c.teacherErr != nil {
		return 0, c.teacherErr
	}
	return c.teacherID, nil
}

func (c *fakeWPClient) FindTagID(_ context.Context, name string) (int64, error) {
	if c.tagErr != nil {
		return 0, c.tagErr
	}
	id, ok := c.tagIDs[name]
	if !ok {
		return 0, &wordpress.ErrTermNotFound{Taxonomy: "метки", Name: name}
	}
	return id, nil
}

// EnsureTag повторяет поведение живой интеграции: сначала поиск, ненайденная метка
// заводится и с этого момента находится поиском — как и в WordPress, где одноимённая метка
// в таксономии существовать дважды не может.
func (c *fakeWPClient) EnsureTag(ctx context.Context, name string) (wordpress.Tag, error) {
	id, err := c.FindTagID(ctx, name)
	if err == nil {
		return wordpress.Tag{ID: id, Name: name}, nil
	}
	var notFound *wordpress.ErrTermNotFound
	if !errors.As(err, &notFound) {
		return wordpress.Tag{}, err
	}
	if c.createTagErr != nil {
		return wordpress.Tag{}, c.createTagErr
	}
	if c.nextTagID == 0 {
		c.nextTagID = 90000
	}
	c.nextTagID++
	if c.tagIDs == nil {
		c.tagIDs = map[string]int64{}
	}
	c.tagIDs[name] = c.nextTagID
	c.createdTags = append(c.createdTags, name)
	return wordpress.Tag{ID: c.nextTagID, Name: name, Created: true}, nil
}

// storedTaxonomy повторяет умолчание интеграции: пустое имя означает встроенную category.
func storedTaxonomy(taxonomy string) string {
	if taxonomy == "" {
		return "category"
	}
	return taxonomy
}

func (c *fakeWPClient) UploadMedia(_ context.Context, file wordpress.MediaFile) (wordpress.UploadedMedia, error) {
	c.uploaded = append(c.uploaded, file)
	if c.uploadErr != nil {
		return wordpress.UploadedMedia{}, c.uploadErr
	}
	return wordpress.UploadedMedia{
		AttachmentID: c.attachmentID,
		URL:          "https://example.test/wp-content/uploads/2026/08/" + file.Name,
		Type:         file.MIMEType,
		Title:        file.Title,
		AltText:      file.AltText,
	}, nil
}

// CreatePost по умолчанию возвращает запись, которая полностью совпадает с отправленным.
// Расхождение вносится через dropField — так проверяется молча отброшенный ключ.
func (c *fakeWPClient) CreatePost(_ context.Context, payload wordpress.PostPayload) (int64, error) {
	c.created = append(c.created, payload)
	c.uploadsAtCreate = len(c.uploaded)
	if c.createErr != nil {
		return 0, c.createErr
	}
	stored := wordpress.StoredPost{
		ID: 21602, Title: payload.Title, Status: payload.Status, ContentHTML: payload.ContentHTML,
		// Слаг подставная площадка возвращает тем же, каким его отправили: занятый адрес
		// WordPress дополнил бы числом, и это отдельный случай — он проверен у сверки.
		Slug: payload.Slug,
		Link: "https://example.test/blog/razryady/",
		// Таксономия берётся та же, что уйдёт в запросе: пустая означает встроенную
		// category — ровно так её подставляет интеграция, и сверка ищет термины по ней же.
		TermIDs: map[string][]int64{
			storedTaxonomy(payload.CategoryTaxonomy): {payload.CategoryID},
			"post_tag":                               payload.TagIDs,
		},
		ThumbnailID: payload.ThumbnailID,
		Fields:      map[string]string{},
	}
	if c.dropThumbnail {
		stored.ThumbnailID = 0
	}
	for _, field := range payload.Fields {
		if field.Key == c.dropField {
			continue
		}
		stored.Fields[field.Key] = field.Value
	}
	c.stored = stored
	return stored.ID, nil
}

func (c *fakeWPClient) GetPost(context.Context, int64) (wordpress.StoredPost, error) {
	return c.stored, c.getErr
}

type fakeWPWriter struct {
	files map[string]string
}

func (w *fakeWPWriter) Read(path string) (string, error) {
	content, ok := w.files[path]
	if !ok {
		return "", fmt.Errorf("нет файла %s", path)
	}
	return content, nil
}

func (w *fakeWPWriter) Exists(path string) bool {
	_, ok := w.files[path]
	return ok
}

// fakeWPImages подменяет каталог обложек. Диска в тестах потока публикации нет намеренно:
// проверяется порядок шагов, а чтение файла проверено отдельно, у самого источника.
type fakeWPImages struct {
	image     wordPressImage
	locateErr error
	loadErr   error
	bits      []byte
	located   []string
	loaded    []wordPressImage
}

func (i *fakeWPImages) Locate(externalID, _ string) (wordPressImage, error) {
	i.located = append(i.located, externalID)
	if i.locateErr != nil {
		return wordPressImage{}, i.locateErr
	}
	return i.image, nil
}

func (i *fakeWPImages) Load(image wordPressImage) (wordpress.MediaFile, error) {
	i.loaded = append(i.loaded, image)
	if i.loadErr != nil {
		return wordpress.MediaFile{}, i.loadErr
	}
	return wordpress.MediaFile{Name: image.MediaName, MIMEType: image.MIMEType, Bits: i.bits}, nil
}

type fakeWPResultBuilder struct {
	built []string
	err   error
}

func (b *fakeWPResultBuilder) Build(_ context.Context, externalID string) (articleoutput.ArticlePaths, error) {
	b.built = append(b.built, externalID)
	return articleoutput.ArticlePaths{}, b.err
}

func readyWPPublicationInput() article.PublicationInput {
	return article.PublicationInput{
		Article: article.Article{
			ID: 11, ExternalID: "16", Title: "Разряды газосварщиков: категории и зарплата", Status: "completed",
			Slug: "razryady-gazosvarshchikov",
		},
		Publication:     article.Publication{Status: article.WordPressNotPublished},
		Category:        "Сварка, слесарка и металлообработка",
		Tags:            "Газосварщик, Повышение квалификации",
		Keyword:         "разряды газосварщиков",
		MetaDescription: "Какие категории существуют.",
		Header:          "Разряды газосварщиков: какие бывают",
		TLDR:            "От разряда зависит сложность работ.",
		FAQ:             "Вопрос: Сколько разрядов?\nОтвет: Пять — со 2-го по 6-й.\nВопрос: Где работает?\nОтвет: В нефтегазовой отрасли.",
		HTMLPath:        testWPHTMLPath,
	}
}

func newWPImages() *fakeWPImages {
	return &fakeWPImages{
		image: wordPressImage{
			Path:      "input/pprof_1/images/16.webp",
			MediaName: "razryady-gazosvarshchikov.webp",
			MIMEType:  "image/webp",
			Size:      2 << 20,
		},
		bits: []byte{0x52, 0x49, 0x46, 0x46},
	}
}

func newWPPublishDeps() (wordPressPublishDeps, *fakeWPRepository, *fakeWPClient, *fakeWPResultBuilder, *bytes.Buffer) {
	repository := &fakeWPRepository{input: readyWPPublicationInput()}
	client := &fakeWPClient{
		categoryID:   2575,
		tagIDs:       map[string]int64{"Газосварщик": 1801, "Повышение квалификации": 1251},
		attachmentID: 21610,
	}
	builder := &fakeWPResultBuilder{}
	out := &bytes.Buffer{}
	deps := wordPressPublishDeps{
		client: client,
		// Раскладка статьи блога: её проверяет большинство тестов публикации, потому что
		// именно с ней работают task_1 и pprof_1.
		mapping:     blogWordPressMapping{},
		repository:  repository,
		writer:      &fakeWPWriter{files: map[string]string{testWPHTMLPath: testWPArticleHTML, testWPResultPath: testWPResultMD}},
		images:      newWPImages(),
		resultBuild: builder,
		logger:      slog.New(slog.DiscardHandler),
		out:         out,
		assumeYes:   true,
	}
	return deps, repository, client, builder, out
}

// --- поток публикации ---

func TestPublishSendsCompletePayload(t *testing.T) {
	deps, repository, client, builder, out := newWPPublishDeps()

	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	if len(client.created) != 1 {
		t.Fatalf("вызовов wp.newPost %d, ожидался один", len(client.created))
	}
	payload := client.created[0]
	if payload.Status != wordpress.PostStatusPublish {
		t.Fatalf("статус записи = %q", payload.Status)
	}
	if payload.Title != "Разряды газосварщиков: категории и зарплата" {
		t.Fatalf("заголовок = %q", payload.Title)
	}
	// Тело статьи уходит как есть: ни обрезки, ни переупаковки.
	if payload.ContentHTML != testWPArticleHTML {
		t.Fatalf("тело записи изменено: %q", payload.ContentHTML)
	}
	if payload.CategoryID != 2575 {
		t.Fatalf("рубрика = %d", payload.CategoryID)
	}
	if len(payload.TagIDs) != 2 || payload.TagIDs[0] != 1801 || payload.TagIDs[1] != 1251 {
		t.Fatalf("метки = %v", payload.TagIDs)
	}
	if payload.ThumbnailID != 21610 {
		t.Fatalf("обложка записи = %d, ожидалось вложение 21610", payload.ThumbnailID)
	}

	fields := map[string]string{}
	for _, field := range payload.Fields {
		fields[field.Key] = field.Value
	}
	expected := map[string]string{
		"blog_tldr":             "От разряда зависит сложность работ.",
		"blog_read":             "9 мин",
		"blog_faq":              "2",
		"blog_faq_0_question":   "Сколько разрядов?",
		"blog_faq_0_answer":     "Пять — со 2-го по 6-й.",
		"blog_faq_1_question":   "Где работает?",
		"blog_faq_1_answer":     "В нефтегазовой отрасли.",
		"prof_title":            "Разряды газосварщиков: какие бывают",
		"prof_blue":             "от 7 000 р",
		"prof_name":             "газосварщик",
		"_yoast_wpseo_focuskw":  "разряды газосварщиков",
		"_yoast_wpseo_title":    "Разряды газосварщиков: категории и зарплата",
		"_yoast_wpseo_metadesc": "Какие категории существуют.",
	}
	for key, want := range expected {
		if fields[key] != want {
			t.Errorf("поле %s = %q, ожидалось %q", key, fields[key], want)
		}
	}
	if len(fields) != len(expected) {
		t.Errorf("полей %d, ожидалось %d: %v", len(fields), len(expected), fields)
	}

	if len(repository.saved) != 2 {
		t.Fatalf("записей в БД %d, ожидались две: сразу после создания и после сверки", len(repository.saved))
	}
	// Первая — без адреса, сразу после создания записи. Это и есть защита от дублей.
	if repository.saved[0] != (savedWPPublication{"16", 21602, ""}) {
		t.Fatalf("первая запись в БД = %+v", repository.saved[0])
	}
	if repository.saved[1].url != "https://example.test/blog/razryady/" {
		t.Fatalf("адрес записи не сохранён: %+v", repository.saved[1])
	}
	if len(builder.built) != 1 || builder.built[0] != "16" {
		t.Fatalf("result.md пересобран %v", builder.built)
	}
	if !strings.Contains(out.String(), "https://example.test/blog/razryady/") {
		t.Fatalf("адрес не показан человеку: %q", out.String())
	}
}

// Задача без стадии info публикуется без TL;DR, FAQ и времени чтения: их у неё нет ни в
// базе, ни в result.md. Пока требование было общим, pprof_2 не доходил до площадки вовсе —
// публикация отказывала на разделе «## Время чтения», которого нет в его шаблоне.
func TestPublishWithoutArticleMetadataSkipsBlogFields(t *testing.T) {
	deps, _, client, _, _ := newWPPublishDeps()
	deps.withoutArticleMetadata = true
	// Ни метаданных в базе, ни раздела времени чтения в result.md — как у коммерческой страницы.
	repository := &fakeWPRepository{input: readyWPPublicationInput()}
	repository.input.TLDR = ""
	repository.input.FAQ = ""
	deps.repository = repository
	deps.writer = &fakeWPWriter{files: map[string]string{
		testWPHTMLPath:   testWPArticleHTML,
		testWPResultPath: "## Название\n\n```text\nОбучение на стропальщика\n```\n",
	}}

	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	if len(client.created) != 1 {
		t.Fatalf("вызовов wp.newPost %d, ожидался один", len(client.created))
	}
	for _, field := range client.created[0].Fields {
		if strings.HasPrefix(field.Key, "blog_") {
			t.Fatalf("поле блоговой статьи ушло в запись: %s = %q", field.Key, field.Value)
		}
	}
	// Поля самой страницы при этом на месте: пропускаются только разделы стадии info.
	fields := map[string]string{}
	for _, field := range client.created[0].Fields {
		fields[field.Key] = field.Value
	}
	if fields["_yoast_wpseo_focuskw"] != "разряды газосварщиков" || fields["prof_name"] != "газосварщик" {
		t.Fatalf("поля страницы потеряны: %v", fields)
	}
}

func TestPublishUploadsCoverBeforeCreatingPost(t *testing.T) {
	deps, _, client, _, _ := newWPPublishDeps()
	images := deps.images.(*fakeWPImages)

	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	if len(client.uploaded) != 1 {
		t.Fatalf("загрузок обложки %d, ожидалась одна", len(client.uploaded))
	}
	// Порядок вынужденный: wp.newPost принимает у обложки только идентификатор, а дописать
	// картинку к готовой записи нельзя — редактирование записей запрещено.
	if client.uploadsAtCreate != 1 {
		t.Fatal("запись создана раньше загрузки обложки")
	}
	file := client.uploaded[0]
	if file.Name != "razryady-gazosvarshchikov.webp" || file.MIMEType != "image/webp" {
		t.Fatalf("загружено %+v: имя в библиотеке берётся из slug статьи", file)
	}
	// Подписи берутся из PostgreSQL как есть: alt — заголовок H1 (article_inputs.header),
	// title — слаг картинки (article_inputs.image_slug). Ничего не вычисляется по файлу и
	// ничего не дописывается после загрузки.
	if file.AltText != "Разряды газосварщиков: какие бывают" {
		t.Fatalf("alt = %q, ожидался заголовок H1 статьи", file.AltText)
	}
	if file.Title != "razryady-gazosvarshchikov" {
		t.Fatalf("title = %q, ожидался image_slug", file.Title)
	}
	if len(file.Bits) == 0 {
		t.Fatal("файл ушёл без содержимого")
	}
	// Файл читается ровно один раз и только в момент публикации.
	if len(images.loaded) != 1 {
		t.Fatalf("обложка прочитана %d раз", len(images.loaded))
	}
}

// Обложка загружается после всех локальных проверок и разрешения справочников: иначе
// непригодная статья оставляла бы в медиабиблиотеке файл, которым никто не пользуется.
func TestPublishDoesNotUploadCoverForUnfitArticle(t *testing.T) {
	deps, repository, client, _, _ := newWPPublishDeps()
	// Непустых меток мало: статья обязана отвалиться на локальной проверке, то есть до
	// единого записывающего запроса — в том числе до заведения меток.
	repository.input.Header = " "
	repository.input.Tags = "Газосварщик, Метка которой нет"

	if err := runWordPressPublish(context.Background(), deps, "16"); err == nil {
		t.Fatal("ожидался отказ")
	}
	if len(client.uploaded) != 0 {
		t.Fatalf("обложка загружена для непригодной статьи: %+v", client.uploaded)
	}
	if len(client.createdTags) != 0 {
		t.Fatalf("непригодная статья завела метки в блоге: %v", client.createdTags)
	}
}

// Метка из Excel, которой в блоге нет, заводится публикацией, и запись получает её
// идентификатор. До этого изменения такая статья не публиковалась вовсе.
func TestPublishCreatesMissingTagAndUsesItsID(t *testing.T) {
	deps, repository, client, _, _ := newWPPublishDeps()
	repository.input.Tags = "Газосварщик, Мастер СМР"

	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	if !reflect.DeepEqual(client.createdTags, []string{"Мастер СМР"}) {
		t.Fatalf("заведены метки %v, ожидалась одна «Мастер СМР»", client.createdTags)
	}
	if len(client.created) != 1 {
		t.Fatalf("записей создано %d", len(client.created))
	}
	tagIDs := client.created[0].TagIDs
	// Существующая метка сохраняет свой идентификатор, новая уходит с заведённым.
	if len(tagIDs) != 2 || tagIDs[0] != 1801 || tagIDs[1] != client.tagIDs["Мастер СМР"] {
		t.Fatalf("метки записи = %v", tagIDs)
	}
}

// Повторная публикация той же метки новых терминов не заводит: EnsureTag ищет до создания,
// и найденная метка возвращает свой идентификатор.
func TestPublishReusesTagCreatedEarlier(t *testing.T) {
	deps, repository, client, _, _ := newWPPublishDeps()
	repository.input.Tags = "Мастер СМР"

	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("первая публикация: %v", err)
	}
	createdID := client.tagIDs["Мастер СМР"]

	// Вторая статья с той же меткой: состояние блога (client) общее, состояние статьи
	// сбрасывается — публиковать дважды одну и ту же запрещено отдельной проверкой.
	deps, repository, _, _, _ = newWPPublishDeps()
	deps.client = client
	repository.input.Tags = "Мастер СМР"
	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("вторая публикация: %v", err)
	}
	if len(client.createdTags) != 1 {
		t.Fatalf("метка заведена повторно: %v", client.createdTags)
	}
	if got := client.created[1].TagIDs; len(got) != 1 || got[0] != createdID {
		t.Fatalf("вторая запись получила метки %v, ожидался %d", got, createdID)
	}
}

func TestPublishStopsWhenCoverUploadFails(t *testing.T) {
	deps, repository, client, builder, _ := newWPPublishDeps()
	client.uploadErr = errors.New("413 Request Entity Too Large")

	err := runWordPressPublish(context.Background(), deps, "16")
	if err == nil {
		t.Fatal("ожидался отказ")
	}
	// Записи в блоге нет — значит, повтор безопасен, и человеку это надо сказать прямо.
	if len(client.created) != 0 {
		t.Fatalf("запись создана без обложки: %+v", client.created)
	}
	if len(repository.saved) != 0 {
		t.Fatalf("состояние публикации изменено: %+v", repository.saved)
	}
	if len(builder.built) != 0 {
		t.Fatal("result.md пересобран при неудачной загрузке обложки")
	}
	if !strings.Contains(err.Error(), "обложк") || !strings.Contains(err.Error(), "повторить") {
		t.Fatalf("ошибка не объясняет, что делать: %v", err)
	}
}

// WordPress ответил успехом, но обложку записи не поставил. Дописать её вторым запросом
// нельзя, поэтому публикация неуспешна — ровно как при потерянном поле.
func TestPublishFailsWhenThumbnailWasNotKept(t *testing.T) {
	deps, repository, client, _, _ := newWPPublishDeps()
	client.dropThumbnail = true

	err := runWordPressPublish(context.Background(), deps, "16")
	if err == nil {
		t.Fatal("публикация без обложки признана успешной")
	}
	if !strings.Contains(err.Error(), "post_thumbnail") {
		t.Fatalf("ошибка не называет обложку: %v", err)
	}
	// Отметка остаётся: запись создана, и без неё следующий запуск создал бы дубль. Второе
	// сохранение — адрес записи, он известен сразу после чтения и от сверки не зависит.
	if len(repository.saved) != 2 {
		t.Fatalf("отметка о публикации: %+v", repository.saved)
	}
}

func TestPublishSavesPostIDBeforeVerificationFails(t *testing.T) {
	deps, repository, client, builder, _ := newWPPublishDeps()
	// WordPress ответил успехом, но один ключ не сохранил — ровно тот отказ, ради которого
	// сверка и существует.
	client.dropField = "_yoast_wpseo_focuskw"

	err := runWordPressPublish(context.Background(), deps, "16")
	if err == nil {
		t.Fatal("публикация с несошедшейся сверкой признана успешной")
	}
	if !strings.Contains(err.Error(), "_yoast_wpseo_focuskw") {
		t.Fatalf("ошибка не называет несошедшееся поле: %v", err)
	}
	// Отметка обязана остаться: запись создана, удалить её нельзя, и без отметки следующий
	// запуск создал бы второй пост. Сохранений два: сразу после создания записи — с одним
	// идентификатором, и после чтения — уже с адресом.
	if len(repository.saved) != 2 {
		t.Fatalf("отметка о публикации не сохранена: %+v", repository.saved)
	}
	for _, saved := range repository.saved {
		if saved.postID != 21602 {
			t.Fatalf("отметка о публикации: %+v", repository.saved)
		}
	}
	// Адрес записи сохраняется и при несошедшейся сверке: запись в блоге есть, и человеку
	// нужна ссылка именно тогда, когда идти смотреть глазами.
	if repository.saved[1].url != "https://example.test/blog/razryady/" {
		t.Fatalf("адрес записи не сохранён: %+v", repository.saved)
	}
	// Лист пересобирается тоже: без него ссылка на запись не дойдёт до result.md.
	if len(builder.built) != 1 {
		t.Fatalf("result.md не пересобран: %v", builder.built)
	}
}

// Лишний пробел из книги в заголовок записи не уходит: на странице его не видно, но в теге
// title и в выдаче он остаётся, а править заголовок опубликованной записи приложение не умеет.
func TestPublishCollapsesSpacesInTitle(t *testing.T) {
	deps, repository, client, _, _ := newWPPublishDeps()
	repository.input.Article.Title = "Косметолог  - дистанционное  обучение"

	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	if got := client.created[0].Title; got != "Косметолог - дистанционное обучение" {
		t.Fatalf("заголовок записи = %q", got)
	}
}

func TestPublishDoesNotRetryAndTellsWhatToDo(t *testing.T) {
	deps, repository, client, _, _ := newWPPublishDeps()
	client.createErr = errors.New("соединение разорвано")

	err := runWordPressPublish(context.Background(), deps, "16")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if len(client.created) != 1 {
		t.Fatalf("попыток создания %d, повтор запрещён", len(client.created))
	}
	if len(repository.saved) != 0 {
		t.Fatalf("в БД что-то записано после неудачного создания: %+v", repository.saved)
	}
	// Исход неизвестен, поэтому человеку называется способ не получить дубль.
	if !strings.Contains(err.Error(), "mark-published") {
		t.Fatalf("ошибка не подсказывает mark-published: %v", err)
	}
}

func TestPublishRefusesBeforeTouchingWordPress(t *testing.T) {
	cases := map[string]func(*wordPressPublishDeps, *fakeWPRepository){
		"уже опубликована нашим publisher": func(_ *wordPressPublishDeps, r *fakeWPRepository) {
			postID := int64(21593)
			r.input.Publication = article.Publication{Status: article.WordPressPublished, PostID: &postID}
		},
		// Дубль одинаково недопустим и для записи, привязанной вручную.
		"привязана вручную": func(_ *wordPressPublishDeps, r *fakeWPRepository) {
			postID := int64(21593)
			r.input.Publication = article.Publication{Status: article.WordPressLinked, PostID: &postID}
		},
		"не completed": func(_ *wordPressPublishDeps, r *fakeWPRepository) {
			r.input.Article.Status = "processing"
		},
		"нет article.html": func(d *wordPressPublishDeps, _ *fakeWPRepository) {
			d.writer = &fakeWPWriter{files: map[string]string{testWPResultPath: testWPResultMD}}
		},
		"нет result.md": func(d *wordPressPublishDeps, _ *fakeWPRepository) {
			d.writer = &fakeWPWriter{files: map[string]string{testWPHTMLPath: testWPArticleHTML}}
		},
		"в result.md нет времени чтения": func(d *wordPressPublishDeps, _ *fakeWPRepository) {
			d.writer = &fakeWPWriter{files: map[string]string{
				testWPHTMLPath: testWPArticleHTML, testWPResultPath: "## HTML\n\n```text\nx\n```\n"}}
		},
		// Метку, которой в блоге нет, публикация заводит сама. Отказом остаётся только
		// отказ площадки её завести: тогда запись создавать не из чего.
		"площадка не дала завести метку": func(d *wordPressPublishDeps, r *fakeWPRepository) {
			r.input.Tags = "Газосварщик, Метка которой нет"
			d.client.(*fakeWPClient).createTagErr = errors.New("403 Forbidden")
		},
		// Записи без картинки в блоге видно сразу, а поставить её потом нельзя: публикация
		// обязана отбиться до первого запроса, а не оставить статью без обложки.
		"нет обложки": func(d *wordPressPublishDeps, _ *fakeWPRepository) {
			images := newWPImages()
			images.locateErr = errors.New("нет обложки статьи 16")
			d.images = images
		},
		// Без slug картинке неоткуда взять title, без H1 — alt. Загружать картинку без
		// подписей нельзя: исправить вложение приложение не умеет.
		"нет image_slug": func(_ *wordPressPublishDeps, r *fakeWPRepository) {
			r.input.Article.Slug = ""
		},
		"нет заголовка H1": func(_ *wordPressPublishDeps, r *fakeWPRepository) {
			r.input.Header = " "
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			deps, repository, client, builder, _ := newWPPublishDeps()
			mutate(&deps, repository)

			if err := runWordPressPublish(context.Background(), deps, "16"); err == nil {
				t.Fatal("ожидался отказ")
			}
			if len(client.created) != 0 {
				t.Fatalf("в WordPress ушла запись, хотя статья не годна: %d", len(client.created))
			}
			if len(client.uploaded) != 0 {
				t.Fatalf("в WordPress ушла обложка, хотя статья не годна: %d", len(client.uploaded))
			}
			if len(repository.saved) != 0 {
				t.Fatalf("состояние публикации изменено: %+v", repository.saved)
			}
			if len(builder.built) != 0 {
				t.Fatal("result.md пересобран при отказе")
			}
		})
	}
}

func TestPublishSurvivesResultRebuildFailure(t *testing.T) {
	deps, repository, _, builder, _ := newWPPublishDeps()
	builder.err = errors.New("шаблон не читается")

	// За публикацию уже заплачено, и побочный шаг не имеет права её уронить.
	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("отказ пересборки result.md уронил публикацию: %v", err)
	}
	if len(repository.saved) != 2 {
		t.Fatalf("состояние публикации не сохранено: %+v", repository.saved)
	}
}

// --- массовая публикация ---

func TestPublishAllStopsOnFirstFailure(t *testing.T) {
	deps, repository, _, _, out := newWPPublishDeps()
	repository.publishable = []string{"16", "17", "18"}
	var attempted []string
	publish := func(_ context.Context, externalID string) error {
		attempted = append(attempted, externalID)
		if externalID == "17" {
			return errors.New("исход неизвестен")
		}
		return nil
	}

	err := runWordPressPublishAll(context.Background(), deps, publish)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	// Отказ с неизвестным исходом требует человека, а не следующей статьи.
	if len(attempted) != 2 || attempted[1] != "17" {
		t.Fatalf("обработаны %v, ожидалась остановка на 17", attempted)
	}
	if !strings.Contains(err.Error(), "17") {
		t.Fatalf("ошибка не называет статью: %v", err)
	}
	if !strings.Contains(out.String(), "необратима") {
		t.Fatalf("человека не предупредили о необратимости: %q", out.String())
	}
	// Итог печатается и при остановке: сколько записей уже создано, на ком встали, почему
	// и что осталось нетронутым.
	report := out.String()
	for _, want := range []string{
		"Опубликовано статей: 1 из 3",
		"Остановлено на статье 17",
		"исход неизвестен",
		"Не тронуты: 18",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("в итоге нет %q: %s", want, report)
		}
	}
}

func TestPublishAllRefusesWithoutConfirmation(t *testing.T) {
	deps, repository, _, _, out := newWPPublishDeps()
	repository.publishable = []string{"16"}
	deps.assumeYes = false
	deps.interactive = true
	deps.in = strings.NewReader("нет\n")
	var attempted int

	err := runWordPressPublishAll(context.Background(), deps, func(context.Context, string) error {
		attempted++
		return nil
	})
	if err != nil {
		t.Fatalf("отказ подтверждения не ошибка: %v", err)
	}
	if attempted != 0 {
		t.Fatalf("опубликовано %d статей без подтверждения", attempted)
	}
	if !strings.Contains(out.String(), "отменена") {
		t.Fatalf("отмена не показана: %q", out.String())
	}
}

func TestPublishAllOnEmptySelectionDoesNothing(t *testing.T) {
	deps, _, _, _, out := newWPPublishDeps()
	var attempted int

	err := runWordPressPublishAll(context.Background(), deps, func(context.Context, string) error {
		attempted++
		return nil
	})
	if err != nil {
		t.Fatalf("пустая выборка: %v", err)
	}
	if attempted != 0 {
		t.Fatalf("опубликовано %d статей при пустой выборке", attempted)
	}
	if !strings.Contains(out.String(), "нечего") {
		t.Fatalf("вывод: %q", out.String())
	}
}

// --- отметка вручную ---

func TestMarkPublishedWithoutPostDoesNotTouchWordPress(t *testing.T) {
	deps, repository, client, _, out := newWPPublishDeps()

	if err := runWordPressMarkPublished(context.Background(), deps, "16", 0); err != nil {
		t.Fatalf("привязка: %v", err)
	}
	if len(repository.linked) != 1 || repository.linked[0] != (savedWPPublication{"16", 0, ""}) {
		t.Fatalf("привязано %+v", repository.linked)
	}
	if len(client.created) != 0 {
		t.Fatal("mark-published создала запись в WordPress")
	}
	if !strings.Contains(out.String(), "В WordPress ничего не отправлялось") {
		t.Fatalf("вывод: %q", out.String())
	}
}

func TestMarkPublishedWithPostReadsItAndStoresURL(t *testing.T) {
	deps, repository, client, _, out := newWPPublishDeps()
	client.stored = wordpress.StoredPost{ID: 21593, Link: "https://example.test/blog/razryady/"}

	if err := runWordPressMarkPublished(context.Background(), deps, "16", 21593); err != nil {
		t.Fatalf("привязка: %v", err)
	}
	// Запрос ровно один и только читающий: ничего не создаётся и не меняется.
	if len(client.created) != 0 {
		t.Fatal("mark-published создала запись в WordPress")
	}
	want := savedWPPublication{"16", 21593, "https://example.test/blog/razryady/"}
	if len(repository.linked) != 1 || repository.linked[0] != want {
		t.Fatalf("привязано %+v, ожидалось %+v", repository.linked, want)
	}
	if !strings.Contains(out.String(), "linked") {
		t.Fatalf("состояние не названо человеку: %q", out.String())
	}
}

func TestMarkPublishedRefusesUnknownPost(t *testing.T) {
	deps, repository, client, _, _ := newWPPublishDeps()
	client.getErr = errors.New("404")

	if err := runWordPressMarkPublished(context.Background(), deps, "16", 999999); err == nil {
		t.Fatal("привязка к несуществующей записи прошла")
	}
	if len(repository.linked) != 0 {
		t.Fatalf("состояние изменено при неудачном чтении записи: %+v", repository.linked)
	}
}

func TestMarkPublishedIsIdempotent(t *testing.T) {
	for _, status := range []string{article.WordPressPublished, article.WordPressLinked} {
		t.Run(status, func(t *testing.T) {
			deps, repository, _, _, out := newWPPublishDeps()
			postID := int64(21593)
			repository.input.Publication = article.Publication{Status: status, PostID: &postID}

			if err := runWordPressMarkPublished(context.Background(), deps, "16", 0); err != nil {
				t.Fatalf("повторная привязка: %v", err)
			}
			if len(repository.linked) != 0 {
				t.Fatal("уже привязанная статья перезаписана")
			}
			if !strings.Contains(out.String(), "уже помечена") {
				t.Fatalf("вывод: %q", out.String())
			}
		})
	}
}

// --- разбор result.md ---

func TestFencedSectionValue(t *testing.T) {
	cases := map[string]struct {
		content string
		header  string
		want    string
	}{
		"обычный раздел":      {testWPResultMD, "## Время чтения", "9 мин"},
		"раздела нет":         {testWPResultMD, "## Ссылка на статью", ""},
		"раздел пуст":         {"## Время чтения\n\n## HTML\n\n```text\nx\n```\n", "## Время чтения", ""},
		"пустая ограда":       {"## Время чтения\n\n```text\n```\n", "## Время чтения", ""},
		"значение в конце":    {"## Время чтения\n\n```text\n12 мин\n```", "## Время чтения", "12 мин"},
		"похожий заголовок":   {"## Время чтения статьи\n\n```text\n5 мин\n```\n", "## Время чтения", ""},
		"незакрытая ограда":   {"## Время чтения\n\n```text\n7 мин\n", "## Время чтения", "7 мин"},
		"переводы строк CRLF": {"## Время чтения\r\n\r\n```text\r\n8 мин\r\n```\r\n", "## Время чтения", "8 мин"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := fencedSectionValue(testCase.content, testCase.header); got != testCase.want {
				t.Fatalf("получено %q, ожидалось %q", got, testCase.want)
			}
		})
	}
}

func TestProfessionNameLowercasesFirstTag(t *testing.T) {
	// Правило проверено на одиннадцати записях, заполненных вручную: первая метка совпала
	// с проставленным человеком prof_name в десяти из них.
	cases := map[string][]string{
		"газосварщик": {"Газосварщик", "Обучение рабочим профессиям"},
		"машинист компрессорных установок": {"Машинист компрессорных установок"},
		"кассир-операционист":              {" Кассир-операционист "},
		"":                                 nil,
	}
	for want, tags := range cases {
		if got := professionName(tags); got != want {
			t.Errorf("для %q получено %q, ожидалось %q", tags, got, want)
		}
	}
}

// --- сухой прогон ---

// planFieldValue достаёт значение строки отчёта вида «  имя   значение».
func planFieldValue(report, field string) string {
	for _, line := range strings.Split(report, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, field+" ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, field))
		}
	}
	return ""
}

func TestPublishPlanShowsPayloadAndWritesNothing(t *testing.T) {
	deps, repository, client, builder, out := newWPPublishDeps()

	if err := runWordPressPublishPlan(context.Background(), deps, "16"); err != nil {
		t.Fatalf("сухой прогон: %v", err)
	}
	// Смысл команды в том, что она безопасна: ни записи в блоге, ни строки в БД.
	if len(client.created) != 0 {
		t.Fatal("сухой прогон создал запись в WordPress")
	}
	if len(repository.saved) != 0 || len(repository.linked) != 0 {
		t.Fatalf("сухой прогон изменил состояние: saved=%+v linked=%+v", repository.saved, repository.linked)
	}
	if len(builder.built) != 0 {
		t.Fatal("сухой прогон пересобрал result.md")
	}
	if len(client.uploaded) != 0 {
		t.Fatal("сухой прогон загрузил обложку")
	}
	// Файл даже не читается: показать путь и вес можно, не поднимая в память мегабайты.
	if loaded := len(deps.images.(*fakeWPImages).loaded); loaded != 0 {
		t.Fatalf("сухой прогон прочитал обложку %d раз", loaded)
	}

	report := out.String()
	// Подписи показываются целиком и без обрезки: это то, что увидят читатель и поисковик.
	if got := planFieldValue(report, "media.alt_text"); got != "Разряды газосварщиков: какие бывают" {
		t.Errorf("alt в отчёте = %q, ожидался заголовок H1", got)
	}
	if got := planFieldValue(report, "media.title"); got != "razryady-gazosvarshchikov" {
		t.Errorf("title в отчёте = %q, ожидался image_slug", got)
	}
	for _, want := range []string{
		"В WordPress ничего не отправлено",
		"Разряды газосварщиков: категории и зарплата",
		"publish",
		`2575  «Сварка, слесарка и металлообработка»`,
		`1801 «Газосварщик»`,
		"custom_fields (13)",
		"blog_faq_1_answer",
		"_yoast_wpseo_metadesc",
		"prof_name",
		"9 мин",
		"post_thumbnail",
		"input/pprof_1/images/16.webp",
		"razryady-gazosvarshchikov.webp",
		"2.0 МБ",
		"media.alt_text",
		"media.title",
		"готова к публикации",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}
}

func TestPublishPlanReportsWhyArticleIsNotReady(t *testing.T) {
	deps, _, client, _, out := newWPPublishDeps()
	// Ровно тот отказ, ради которого сухой прогон и нужен: рубрики нет в блоге, а заводить
	// рубрики запрещено — узнать об этом надо до необратимой команды, а не в её середине.
	client.categoryErr = &wordpress.ErrTermNotFound{Taxonomy: "рубрики", Name: "Строительство и ремонт"}

	err := runWordPressPublishPlan(context.Background(), deps, "16")
	if err == nil {
		t.Fatal("сухой прогон не заметил непригодную статью")
	}
	if len(client.created) != 0 {
		t.Fatal("сухой прогон создал запись в WordPress")
	}
	if !strings.Contains(out.String(), "НЕ готова") {
		t.Fatalf("отчёт не называет статью непригодной: %q", out.String())
	}
}

func TestPublishPlanAllSeparatesReadyFromBroken(t *testing.T) {
	deps, repository, client, _, out := newWPPublishDeps()
	repository.publishable = []string{"16"}

	if err := runWordPressPublishPlanAll(context.Background(), deps); err != nil {
		t.Fatalf("массовый сухой прогон: %v", err)
	}
	if len(client.created) != 0 {
		t.Fatal("массовый сухой прогон создал запись")
	}
	if !strings.Contains(out.String(), "Готовы к публикации: 1 из 1") {
		t.Fatalf("отчёт: %q", out.String())
	}

	// Непригодная статья обязана давать ненулевой код: сухой прогон — это проверка перед
	// необратимой командой, и «часть отвалится» успехом не считается.
	deps, repository, client, _, out = newWPPublishDeps()
	repository.publishable = []string{"16"}
	client.categoryErr = &wordpress.ErrTermNotFound{Taxonomy: "рубрики", Name: "Строительство и ремонт"}
	err := runWordPressPublishPlanAll(context.Background(), deps)
	if err == nil {
		t.Fatal("массовый сухой прогон с непригодной статьёй завершился успехом")
	}
	if !strings.Contains(err.Error(), "16") {
		t.Fatalf("ошибка не называет статью: %v", err)
	}
	if !strings.Contains(out.String(), "НЕ ГОТОВА") {
		t.Fatalf("отчёт: %q", out.String())
	}
}

// Сухой прогон обязан остаться read-only: метку, которой нет, он показывает как будущую,
// но не заводит — иначе обещание «в WordPress ничего не отправлено» перестало бы быть правдой.
func TestPublishPlanShowsNewTagWithoutCreatingIt(t *testing.T) {
	deps, repository, client, _, out := newWPPublishDeps()
	repository.input.Tags = "Газосварщик, Мастер СМР"

	if err := runWordPressPublishPlan(context.Background(), deps, "16"); err != nil {
		t.Fatalf("сухой прогон: %v", err)
	}
	if len(client.createdTags) != 0 {
		t.Fatalf("сухой прогон завёл метки: %v", client.createdTags)
	}
	report := out.String()
	for _, want := range []string{"новая «Мастер СМР»", "1801 «Газосварщик»", "publish заведёт их"} {
		if !strings.Contains(report, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, report)
		}
	}
}

func TestPublishPlanOnEmptySelection(t *testing.T) {
	deps, _, _, _, out := newWPPublishDeps()

	if err := runWordPressPublishPlanAll(context.Background(), deps); err != nil {
		t.Fatalf("пустая выборка: %v", err)
	}
	if !strings.Contains(out.String(), "нечего") {
		t.Fatalf("отчёт: %q", out.String())
	}
}

// Слаг записи берётся из книги импорта и приводится к тому виду, который WordPress не станет
// переписывать: адрес из русского заголовка получается на всю строку, ради этого поле и есть.
func TestWordPressSlug(t *testing.T) {
	for value, want := range map[string]string{
		"obyazannosti-gornorabochego":      "obyazannosti-gornorabochego",
		"  Obuchenie_Na Gazosvarshchika  ": "obuchenie-na-gazosvarshchika",
		"perepodgotovka//gornorabochij/":   "perepodgotovka-gornorabochij",
		"обязанности":                      "",
		"":                                 "",
	} {
		if got := wordPressSlug(value); got != want {
			t.Errorf("wordPressSlug(%q) = %q, ожидалось %q", value, got, want)
		}
	}
}

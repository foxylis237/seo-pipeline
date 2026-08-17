package demo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

const (
	testExternalID = "37"
	testSlug       = "kak-stat-logopedom"
	testDirectory  = testExternalID + "-" + testSlug
)

type fakeRepository struct {
	result      article.ResultInput
	resultErr   error
	saved       article.SavedGenerationInput
	savedErr    error
	research    article.GenerationInput
	researchErr error
	demoInput   article.GenerationInput
	demoErr     error
}

func (r *fakeRepository) GetResultInput(context.Context, string) (article.ResultInput, error) {
	return r.result, r.resultErr
}

func (r *fakeRepository) GetSavedGenerationInput(context.Context, string) (article.SavedGenerationInput, error) {
	return r.saved, r.savedErr
}

func (r *fakeRepository) GetGenerationInput(context.Context, string) (article.GenerationInput, error) {
	return r.research, r.researchErr
}

func (r *fakeRepository) GetDemoGenerationInput(context.Context, string) (article.GenerationInput, error) {
	return r.demoInput, r.demoErr
}

type fakeGenerator struct {
	answers  map[string]string
	failures map[string]error
	prepared []string
	prompted map[string]any
	calls    []string
}

func newFakeGenerator() *fakeGenerator {
	return &fakeGenerator{
		answers:  map[string]string{"structure": "СГЕНЕРИРОВАННАЯ СТРУКТУРА", "article": "СГЕНЕРИРОВАННАЯ СТАТЬЯ", "info": "СГЕНЕРИРОВАННАЯ ИНФОРМАЦИЯ"},
		failures: map[string]error{},
		prompted: map[string]any{},
	}
}

func (g *fakeGenerator) Prepare(call llm.Call) (llm.PreparedCall, error) {
	g.prepared = append(g.prepared, call.Stage)
	g.prompted[call.Stage] = call.Data
	if err := g.failures["prepare:"+call.Stage]; err != nil {
		return llm.PreparedCall{}, err
	}
	return llm.PreparedCall{Prompt: fmt.Sprintf("промпт стадии %s: %+v", call.Stage, call.Data)}, nil
}

func (g *fakeGenerator) Generate(_ context.Context, call llm.Call) (llm.RoutedResponse, error) {
	g.calls = append(g.calls, call.Stage)
	if err := g.failures[call.Stage]; err != nil {
		return llm.RoutedResponse{}, err
	}
	return llm.RoutedResponse{Response: llm.Response{Text: g.answers[call.Stage]}, Prompt: "промпт " + call.Stage}, nil
}

type fakeResult struct {
	rendered    string
	err         error
	calls       int
	articleText string
	// metadata — набор, с которым demo пришёл за result.md. nil означает «источник — БД».
	metadata    *article.ArticleInfo
	metadataSet bool
}

func (r *fakeResult) RenderForDemo(_ context.Context, _, articleText string, metadata *article.ArticleInfo) (string, error) {
	r.calls++
	r.articleText = articleText
	r.metadata, r.metadataSet = metadata, true
	return r.rendered, r.err
}

// fakePreparer подменяет боевой prepare: он «собирает» research и кладёт его в репозиторий,
// как это сделал бы Keys.so с Arsenkin через PostgreSQL.
type fakePreparer struct {
	calls      int
	err        error
	repository *fakeRepository
	collected  article.GenerationInput
}

func (p *fakePreparer) Prepare(context.Context, string) error {
	p.calls++
	if p.err != nil {
		return p.err
	}
	p.repository.research, p.repository.researchErr = p.collected, nil
	return nil
}

type fixture struct {
	root       string
	builder    *Builder
	repository *fakeRepository
	generator  *fakeGenerator
	result     *fakeResult
	preparer   *fakePreparer
	paths      articleoutput.ArticlePaths
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	paths, err := articleoutput.Paths(testExternalID, testSlug)
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakeRepository{
		result: article.ResultInput{
			Article:  article.Article{ID: 7, ExternalID: testExternalID, Title: "Как стать логопедом", Slug: testSlug, Status: "pending"},
			Category: "Профессии", Author: "Редакция", Keyword: "логопед", Header: "H1", Professions: "Логопед — /logoped",
		},
		saved: article.SavedGenerationInput{
			Article:     article.Article{ID: 7, ExternalID: testExternalID, Slug: testSlug},
			Professions: "Логопед — /logoped", Links: "Дополнительно — /extra",
		},
		research: article.GenerationInput{
			Article:             article.Article{ID: 7, ExternalID: testExternalID, Title: "Как стать логопедом", Slug: testSlug},
			CompetitorStructure: "H2 Кто такой логопед",
			WordstatKeywords:    []article.KeywordFrequency{{Query: "логопед обучение", Frequency: 1200}},
			LSIWords:            []string{"дефектология"},
			Professions:         "Логопед — /logoped", Links: "Дополнительно — /extra",
		},
	}
	generator := newFakeGenerator()
	renderer := &fakeResult{rendered: "## Название\n\n```text\nКак стать логопедом\n```\n"}
	preparer := &fakePreparer{repository: repository, collected: repository.research}
	mergedPrompt := filepath.Join("..", "..", "..", "tasks", "task_1", "prompts", FixLinksHTMLPromptFile)
	builder := NewBuilder(root, mergedPrompt, repository, articleoutput.NewWriter(root), renderer, generator, preparer,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &fixture{
		root: root, builder: builder, repository: repository,
		generator: generator, result: renderer, preparer: preparer, paths: paths,
	}
}

// writeProduction кладёт боевой артефакт статьи на диск.
func (f *fixture) writeProduction(t *testing.T, relativePath, content string) {
	t.Helper()
	path := filepath.Join(f.root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCompleteProduction воспроизводит статью, прошедшую пайплайн целиком.
func (f *fixture) writeCompleteProduction(t *testing.T) {
	t.Helper()
	f.writeProduction(t, f.paths.StructurePath, "БОЕВАЯ СТРУКТУРА")
	f.writeProduction(t, f.paths.StructurePromptPath, "БОЕВОЙ ПРОМПТ СТРУКТУРЫ")
	f.writeProduction(t, f.paths.ArticlePath, "БОЕВАЯ СТАТЬЯ")
	f.writeProduction(t, f.paths.ArticlePromptPath, "БОЕВОЙ ПРОМПТ СТАТЬИ")
	f.writeProduction(t, f.paths.ArticleInfoPath, "БОЕВАЯ ИНФОРМАЦИЯ")
	f.writeProduction(t, f.paths.ArticleInfoPromptPath, "БОЕВОЙ ПРОМПТ ИНФОРМАЦИИ")
	f.writeProduction(t, testDirectory+"/prepare/keysso.json", `{"cleaned_count":12}`)
	f.repository.saved.StructurePath = f.paths.StructurePath
	f.repository.result.ArticlePath = f.paths.ArticlePath
}

func (f *fixture) demoFile(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(f.root, testDirectory, FolderName, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("файл DEMO %s не прочитан: %v", name, err)
	}
	return string(content)
}

func (f *fixture) demoFiles(t *testing.T) []string {
	t.Helper()
	demoRoot := filepath.Join(f.root, testDirectory, FolderName)
	var names []string
	err := filepath.WalkDir(demoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(demoRoot, path)
		if relErr != nil {
			return relErr
		}
		names = append(names, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatalf("каталог DEMO не прочитан: %v", err)
	}
	sort.Strings(names)
	return names
}

// demoRootFiles перечисляет файлы в корне DEMO — то, что видит человек, открыв папку.
func (f *fixture) demoRootFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.root, testDirectory, FolderName))
	if err != nil {
		t.Fatalf("корень DEMO не прочитан: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// snapshotOutsideDemo фиксирует всё, кроме папки DEMO: боевые файлы демо-сборка менять не должна.
func snapshotOutsideDemo(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if entry.IsDir() {
			if entry.Name() == FolderName {
				return filepath.SkipDir
			}
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		snapshot[filepath.ToSlash(relative)] = string(content)
		return nil
	})
	if err != nil {
		t.Fatalf("снимок каталога не собран: %v", err)
	}
	return snapshot
}

func TestBuildReusesCompletedArticleWithoutCallingLLM(t *testing.T) {
	fixture := newFixture(t)
	fixture.repository.result.Article.Status = "completed"
	fixture.writeCompleteProduction(t)
	before := snapshotOutsideDemo(t, fixture.root)

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if len(fixture.generator.calls) != 0 {
		t.Fatalf("LLM вызывался для готовой статьи: %v", fixture.generator.calls)
	}
	want := []string{
		"article_prompt.txt", "fix_links_html_prompt.txt",
		"generated/article.txt", "generated/article_info.txt", "generated/structure.txt",
		"prepare/keysso.json", "prompts/article_info_prompt.txt", "prompts/structure_prompt.txt",
		"result.md",
	}
	if got := fixture.demoFiles(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("состав DEMO = %v, want %v", got, want)
	}
	for name, wantContent := range map[string]string{
		"generated/article.txt":      "БОЕВАЯ СТАТЬЯ",
		"generated/article_info.txt": "БОЕВАЯ ИНФОРМАЦИЯ",
		"article_prompt.txt":         "БОЕВОЙ ПРОМПТ СТАТЬИ",
		"generated/structure.txt":    "БОЕВАЯ СТРУКТУРА",
	} {
		if got := fixture.demoFile(t, name); got != wantContent {
			t.Fatalf("DEMO/%s = %q, want %q", name, got, wantContent)
		}
	}
	if fixture.result.articleText != "БОЕВАЯ СТАТЬЯ" || fixture.result.calls != 1 {
		t.Fatalf("result.md собран из %q за %d вызовов", fixture.result.articleText, fixture.result.calls)
	}
	if got := snapshotOutsideDemo(t, fixture.root); !reflect.DeepEqual(got, before) {
		t.Fatalf("боевые файлы изменились:\nбыло %v\nстало %v", before, got)
	}
}

func TestBuildAssemblesDemoForFailedArticle(t *testing.T) {
	fixture := newFixture(t)
	recorded := "Keys.so timeout"
	step := "article_generation"
	fixture.repository.result.Article.Status = "failed"
	fixture.repository.result.Article.ErrorMessage = &recorded
	fixture.repository.result.Article.CurrentStep = &step
	fixture.writeCompleteProduction(t)

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if len(fixture.generator.calls) != 0 {
		t.Fatalf("LLM вызывался при готовых артефактах: %v", fixture.generator.calls)
	}
	if got := fixture.demoFile(t, "generated/article.txt"); got != "БОЕВАЯ СТАТЬЯ" {
		t.Fatalf("DEMO/generated/article.txt = %q", got)
	}
	if fixture.repository.result.Article.Status != "failed" || *fixture.repository.result.Article.ErrorMessage != recorded {
		t.Fatalf("состояние статьи изменилось: %+v", fixture.repository.result.Article)
	}
}

func TestBuildRunsOnlyTheMissingStage(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)
	if err := os.Remove(filepath.Join(fixture.root, filepath.FromSlash(fixture.paths.ArticleInfoPath))); err != nil {
		t.Fatal(err)
	}

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if !reflect.DeepEqual(fixture.generator.calls, []string{"info"}) {
		t.Fatalf("вызванные стадии = %v, want только info", fixture.generator.calls)
	}
	if got := fixture.demoFile(t, "generated/article_info.txt"); got != "СГЕНЕРИРОВАННАЯ ИНФОРМАЦИЯ" {
		t.Fatalf("DEMO/generated/article_info.txt = %q", got)
	}
	if got := fixture.demoFile(t, "generated/article.txt"); got != "БОЕВАЯ СТАТЬЯ" {
		t.Fatalf("статья перегенерирована: %q", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, filepath.FromSlash(fixture.paths.ArticleInfoPath))); !os.IsNotExist(err) {
		t.Fatalf("недостающий боевой артефакт был создан: %v", err)
	}
}

func TestBuildGeneratesArticleAndInfoFromReusedStructure(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeProduction(t, fixture.paths.StructurePath, "БОЕВАЯ СТРУКТУРА")
	fixture.repository.saved.StructurePath = fixture.paths.StructurePath

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if !reflect.DeepEqual(fixture.generator.calls, []string{"article", "info"}) {
		t.Fatalf("вызванные стадии = %v, want article и info", fixture.generator.calls)
	}
	if got := fixture.demoFile(t, "generated/structure.txt"); got != "БОЕВАЯ СТРУКТУРА" {
		t.Fatalf("структура перегенерирована: %q", got)
	}
	if got := fixture.demoFile(t, "article_prompt.txt"); !strings.Contains(got, "БОЕВАЯ СТРУКТУРА") ||
		!strings.Contains(got, "логопед обучение\t1200") || !strings.Contains(got, "дефектология") {
		t.Fatalf("промпт статьи собран без исходных данных: %q", got)
	}
}

func TestBuildOverwritesPreviousDemo(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)
	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("первый Build() = %v", err)
	}
	stale := filepath.Join(fixture.root, testDirectory, FolderName, "stale.txt")
	if err := os.WriteFile(stale, []byte("остаток прошлой сборки"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.writeProduction(t, fixture.paths.ArticlePath, "ОБНОВЛЁННАЯ СТАТЬЯ")

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("повторный Build() = %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("файл прошлой сборки уцелел: %v", err)
	}
	if got := fixture.demoFile(t, "generated/article.txt"); got != "ОБНОВЛЁННАЯ СТАТЬЯ" {
		t.Fatalf("DEMO/generated/article.txt = %q, want обновлённую статью", got)
	}
}

func TestBuildWritesResultAndPromptsWhenDataIsIncomplete(t *testing.T) {
	fixture := newFixture(t)
	fixture.repository.researchErr = errors.New("research не собран")
	fixture.preparer.err = errors.New("Keys.so недоступен")
	fixture.repository.demoInput = article.GenerationInput{
		Article:     article.Article{ID: 7, ExternalID: testExternalID, Slug: testSlug},
		Professions: "Логопед — /logoped", Links: "Дополнительно — /extra",
	}
	fixture.generator.failures["article"] = errors.New("модель недоступна")

	err := fixture.builder.Build(context.Background(), testExternalID)
	if err == nil {
		t.Fatal("Build() = nil, want ошибку недоступной стадии")
	}

	want := []string{"article_prompt.txt", "fix_links_html_prompt.txt", "result.md"}
	if got := fixture.demoFiles(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("состав DEMO = %v, want %v", got, want)
	}
	if got := fixture.demoFile(t, "result.md"); got != fixture.result.rendered {
		t.Fatalf("result.md = %q", got)
	}
	if fixture.result.articleText != "" {
		t.Fatalf("result.md собран из текста %q, хотя статьи нет", fixture.result.articleText)
	}
}

func TestBuildDoesNotRunPrepareWhenResearchExists(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if fixture.preparer.calls != 0 {
		t.Fatalf("prepare вызван %d раз при готовом research", fixture.preparer.calls)
	}
}

// Отсутствующий research собирается существующим prepare ровно один раз, после чего DEMO
// получает полноценные исходные данные: структура строится из структуры конкурентов, а
// промпт статьи — из ключей и LSI.
func TestBuildRunsPrepareOnceWhenResearchIsMissing(t *testing.T) {
	fixture := newFixture(t)
	fixture.preparer.collected = fixture.repository.research
	fixture.repository.research = article.GenerationInput{}
	fixture.repository.researchErr = errors.New("отсутствует competitor_structure в article_research")

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if fixture.preparer.calls != 1 {
		t.Fatalf("prepare вызван %d раз, want ровно один", fixture.preparer.calls)
	}
	if !reflect.DeepEqual(fixture.generator.calls, []string{"structure", "article", "info"}) {
		t.Fatalf("вызванные стадии = %v, want structure, article и info", fixture.generator.calls)
	}
	if got := fixture.demoFile(t, "generated/structure.txt"); got != "СГЕНЕРИРОВАННАЯ СТРУКТУРА" {
		t.Fatalf("DEMO/generated/structure.txt = %q", got)
	}
	if got := fixture.demoFile(t, "prompts/structure_prompt.txt"); !strings.Contains(got, "H2 Кто такой логопед") {
		t.Fatalf("промпт структуры собран без структуры конкурентов: %q", got)
	}
	if got := fixture.demoFile(t, "article_prompt.txt"); !strings.Contains(got, "логопед обучение\t1200") ||
		!strings.Contains(got, "дефектология") || !strings.Contains(got, "СГЕНЕРИРОВАННАЯ СТРУКТУРА") {
		t.Fatalf("промпт статьи собран без собранных prepare данных: %q", got)
	}
}

func TestBuildRendersMergedFixLinksHTMLPrompt(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	prompt := fixture.demoFile(t, "fix_links_html_prompt.txt")
	for _, requirement := range []string{
		"исправления из предыдущего анализа", // fix
		"Сохрани итоговый объём статьи",      // fix
		"внутреннюю перелинковку",            // линковка
		// Требование к ссылке без URL проверяется по смыслу: промпт переписывают отдельно, и
		// перефразирование при том же требовании тест ронять не должно.
		`href="#"`,                          // линковка
		"HTML-разметку для WordPress",       // html
		`<p class="ds-markdown-paragraph">`, // html
		// Профессии в этот промпт больше не подставляются: {{.Professions}} из него убрали
		// намеренно. Поле по-прежнему передаётся в шаблон, чтобы вернуть его можно было
		// правкой одного промпта, без изменений в Go.
		"Дополнительно — /extra", // подстановка ссылок
	} {
		if !strings.Contains(prompt, requirement) {
			t.Fatalf("объединённый промпт не содержит %q", requirement)
		}
	}
	if strings.Contains(prompt, "{{") {
		t.Fatalf("объединённый промпт не отрендерен: %q", prompt)
	}
}

// storedMetadata — метаданные статьи, уже прошедшей стадию info.
func storedMetadata() (tldr, faq string) {
	return "Логопед ставит речь.", "Вопрос: Где учиться?\nОтвет: В вузе."
}

// Готовые метаданные в PostgreSQL — единственный источник: стадия info повторно не
// запускается, а result.md собирается по данным БД (metadata == nil).
func TestBuildTakesMetadataFromDatabaseWithoutRunningInfo(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)
	tldr, faq := storedMetadata()
	fixture.repository.result.TLDR, fixture.repository.result.FAQ = tldr, faq
	// Боевого article_info.txt нет: даже без файла стадия info не нужна — метаданные уже в БД.
	if err := os.Remove(filepath.Join(fixture.root, filepath.FromSlash(fixture.paths.ArticleInfoPath))); err != nil {
		t.Fatal(err)
	}

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if len(fixture.generator.calls) != 0 {
		t.Fatalf("стадия info запущена при готовых метаданных в БД: %v", fixture.generator.calls)
	}
	if !fixture.result.metadataSet || fixture.result.metadata != nil {
		t.Fatalf("result.md собран с подменой метаданных %+v, want источник БД", fixture.result.metadata)
	}
}

// Метаданных в БД нет — источником становится article_info.txt, разобранный demo целиком.
func TestBuildParsesGeneratedArticleInfoIntoResultMetadata(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)
	if err := os.Remove(filepath.Join(fixture.root, filepath.FromSlash(fixture.paths.ArticleInfoPath))); err != nil {
		t.Fatal(err)
	}
	tldr, faq := storedMetadata()
	fixture.generator.answers["info"] = "TLDR: " + tldr + "\nFAQ:\n" + faq

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if !reflect.DeepEqual(fixture.generator.calls, []string{"info"}) {
		t.Fatalf("вызванные стадии = %v, want только info", fixture.generator.calls)
	}
	metadata := fixture.result.metadata
	if metadata == nil {
		t.Fatal("result.md собран по данным БД, хотя метаданных там нет")
	}
	if metadata.TLDR != tldr || metadata.FAQ != faq {
		t.Fatalf("разобранные метаданные = %+v, want TLDR %q, FAQ %q", metadata, tldr, faq)
	}
	if got := fixture.demoFile(t, "generated/article_info.txt"); !strings.Contains(got, tldr) {
		t.Fatalf("DEMO/generated/article_info.txt = %q", got)
	}
}

// Частично заполненные метаданные в БД не дополняются разбором article_info.txt: источник
// выбирается целиком. Иначе result.md собрался бы из FAQ одного прогона и TL;DR другого.
func TestBuildDoesNotMixDatabaseAndDemoMetadata(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)
	_, faq := storedMetadata()
	fixture.repository.result.FAQ = faq
	// На диске лежит разбираемый article_info.txt с TL;DR: если бы demo дополнял пустые
	// поля БД, они бы отсюда и приехали.
	fixture.writeProduction(t, fixture.paths.ArticleInfoPath, "TLDR: коротко из файла\nFAQ: -")

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if len(fixture.generator.calls) != 0 {
		t.Fatalf("стадия info запущена при метаданных в БД: %v", fixture.generator.calls)
	}
	if !fixture.result.metadataSet || fixture.result.metadata != nil {
		t.Fatalf("метаданные БД дополнены разбором файла: %+v", fixture.result.metadata)
	}
}

// Неразобранный ответ стадии info не теряется: он целиком уходит в TL;DR, а сборка остаётся
// успешной — пустой раздел в result.md заметить труднее, чем текст не по формату.
func TestBuildKeepsUnparsableArticleInfoAsTLDR(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	metadata := fixture.result.metadata
	if metadata == nil || metadata.TLDR != "БОЕВАЯ ИНФОРМАЦИЯ" {
		t.Fatalf("метаданные = %+v, want весь текст article_info.txt в TL;DR", metadata)
	}
}

// Demo собирает метаданные для result.md, но в PostgreSQL их не пишет: интерфейс Repository
// методов записи не объявляет, и сохранённое состояние статьи после сборки то же.
func TestBuildDoesNotWriteMetadataToDatabase(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)
	if err := os.Remove(filepath.Join(fixture.root, filepath.FromSlash(fixture.paths.ArticleInfoPath))); err != nil {
		t.Fatal(err)
	}
	tldr, faq := storedMetadata()
	fixture.generator.answers["info"] = "TLDR: " + tldr + "\nFAQ:\n" + faq
	before := fixture.repository.result

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if !reflect.DeepEqual(fixture.repository.result, before) {
		t.Fatalf("состояние статьи в БД изменилось:\nбыло %+v\nстало %+v", before, fixture.repository.result)
	}
	if _, isWriter := any(fixture.repository).(interface {
		SaveArticleInfo(context.Context, int64, string, article.ArticleInfo) error
	}); isWriter {
		t.Fatal("demo получил репозиторий с методом записи метаданных")
	}
}

// В корне DEMO остаётся ровно то, что открывают руками: результат и два промпта ручного
// чата. Всё промежуточное лежит по подпапкам боевой раскладки.
func TestBuildKeepsOnlyResultAndManualPromptsInDemoRoot(t *testing.T) {
	fixture := newFixture(t)
	fixture.writeCompleteProduction(t)

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	wantRoot := []string{"article_prompt.txt", "fix_links_html_prompt.txt", "result.md"}
	if got := fixture.demoRootFiles(t); !reflect.DeepEqual(got, wantRoot) {
		t.Fatalf("корень DEMO = %v, want %v", got, wantRoot)
	}
	wantAll := []string{
		"article_prompt.txt", "fix_links_html_prompt.txt",
		"generated/article.txt", "generated/article_info.txt", "generated/structure.txt",
		"prepare/keysso.json", "prompts/article_info_prompt.txt", "prompts/structure_prompt.txt",
		"result.md",
	}
	if got := fixture.demoFiles(t); !reflect.DeepEqual(got, wantAll) {
		t.Fatalf("состав DEMO = %v, want %v", got, wantAll)
	}
}

func TestBuildDoesNotReadArtifactsOfAnotherArticle(t *testing.T) {
	fixture := newFixture(t)
	foreign := "41-drugaya-statya/generated/article.txt"
	fixture.writeProduction(t, foreign, "ЧУЖАЯ СТАТЬЯ")
	fixture.repository.result.ArticlePath = foreign

	if err := fixture.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if got := fixture.demoFile(t, "generated/article.txt"); got == "ЧУЖАЯ СТАТЬЯ" {
		t.Fatal("в DEMO попала статья другой статьи")
	}
}

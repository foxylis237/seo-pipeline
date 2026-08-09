package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
)

// articleState — всё, что известно о статье к началу сборки.
type articleState struct {
	externalID  string
	directory   string
	result      article.ResultInput
	saved       article.SavedGenerationInput
	research    article.GenerationInput
	hasResearch bool
	production  articleoutput.ArticlePaths
	// researchErr — несобранный research. Сборку он не прерывает, но попадает в итог,
	// иначе заведомо неполная папка выглядела бы успешной.
	researchErr error
}

func (s articleState) professions() string {
	for _, value := range []string{s.research.Professions, s.saved.Professions, s.result.Professions} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s articleState) links() string {
	if strings.TrimSpace(s.research.Links) != "" {
		return s.research.Links
	}
	return s.saved.Links
}

func (s articleState) title() string { return s.result.Article.Title }

// hasStoredMetadata сообщает, что стадия info уже отработала и её результат лежит в
// PostgreSQL. Признак читается из того же ResultInput, по которому собирается result.md,
// поэтому «есть метаданные» и «они попадут в result.md» — одно и то же условие.
func (s articleState) hasStoredMetadata() bool {
	return strings.TrimSpace(s.result.Tags) != "" ||
		strings.TrimSpace(s.result.TLDR) != "" ||
		strings.TrimSpace(s.result.FAQ) != ""
}

// load собирает состояние статьи из читающих методов репозитория. Отсутствие research или
// сохранённых артефактов — не ошибка: DEMO обязан собраться и для статьи, которая ещё не
// проходила пайплайн.
func (b *Builder) load(ctx context.Context, externalID string) (articleState, error) {
	resultInput, err := b.repository.GetResultInput(ctx, externalID)
	if err != nil {
		return articleState{}, err
	}
	if resultInput.Article.ExternalID != externalID {
		return articleState{}, fmt.Errorf(
			"запрошена статья external_id=%s, а загружена article_id=%d external_id=%s",
			externalID, resultInput.Article.ID, resultInput.Article.ExternalID)
	}
	slug := strings.TrimSpace(resultInput.Article.Slug)
	if slug == "" {
		slug = slugFromArtifactPath(externalID, resultInput.ArticlePath)
	}
	if slug == "" {
		return articleState{}, fmt.Errorf(
			"для статьи external_id %s неизвестен slug: image_slug не задан и сохранённых артефактов нет", externalID)
	}
	resultInput.Article.Slug = slug
	paths, err := articleoutput.Paths(externalID, slug)
	if err != nil {
		return articleState{}, err
	}
	state := articleState{
		externalID: externalID, directory: articleoutput.DirectoryName(externalID, slug),
		result: resultInput, production: paths,
	}
	if saved, savedErr := b.repository.GetSavedGenerationInput(ctx, externalID); savedErr == nil {
		state.saved = saved
	} else {
		b.logger.Warn("сохранённые артефакты статьи не прочитаны", "external_id", externalID,
			"stage", "demo_load", "error", savedErr)
	}
	b.loadResearch(ctx, externalID, &state)
	return state, nil
}

// loadResearch берёт готовый research, а при его отсутствии один раз запускает prepare и
// перечитывает результат. Повторно Keys.so и Arsenkin не дёргаются: собранный research
// остаётся в PostgreSQL и следующей сборке достаётся уже готовым.
func (b *Builder) loadResearch(ctx context.Context, externalID string, state *articleState) {
	research, err := b.repository.GetGenerationInput(ctx, externalID)
	if err != nil && b.preparer != nil {
		if prepareErr := b.preparer.Prepare(ctx, externalID); prepareErr != nil {
			state.researchErr = fmt.Errorf("собрать research для DEMO external_id %s: %w", externalID, prepareErr)
		} else {
			research, err = b.repository.GetGenerationInput(ctx, externalID)
		}
	}
	if err == nil {
		state.research, state.hasResearch = research, true
		return
	}
	if state.researchErr == nil {
		state.researchErr = fmt.Errorf("research статьи external_id %s недоступен: %w", externalID, err)
	}
	b.logger.Warn("research статьи недоступен, стадии без него соберутся частично",
		"external_id", externalID, "stage", "demo_load", "error", err)
	if fallback, fallbackErr := b.repository.GetDemoGenerationInput(ctx, externalID); fallbackErr == nil {
		state.research = fallback
	}
}

// slugFromArtifactPath восстанавливает slug из пути сохранённого артефакта: он всегда
// начинается с каталога <external_id>-<slug>.
func slugFromArtifactPath(externalID, path string) string {
	directory := strings.SplitN(strings.Trim(filepath.ToSlash(path), "/"), "/", 2)[0]
	prefix := externalID + "-"
	if directory == "" || !strings.HasPrefix(directory, prefix) {
		return ""
	}
	return strings.TrimPrefix(directory, prefix)
}

// structure кладёт в DEMO готовую структуру, а при её отсутствии генерирует новую.
func (b *Builder) structure(ctx context.Context, state articleState, staging string) (string, error) {
	if text, found := b.readProduction(state, state.saved.StructurePath); found {
		if err := writeDemoFile(staging, structureFile, text); err != nil {
			return "", err
		}
		b.copyProduction(state, state.production.StructurePromptPath, staging, structurePromptFile)
		return text, nil
	}
	if !state.hasResearch {
		b.logger.Warn("структура пропущена: research не собран", "external_id", state.externalID, "stage", "demo_structure")
		return "", nil
	}
	call := llm.Call{Stage: "structure", ArticleID: state.result.Article.ID, Data: struct {
		Title     string
		Structure string
	}{state.title(), state.research.CompetitorStructure}}
	if err := b.writePrompt(state, staging, structurePromptFile, call); err != nil {
		return "", err
	}
	response, err := b.generator.Generate(ctx, call)
	if err != nil {
		return "", fmt.Errorf("сгенерировать структуру для DEMO external_id %s: %w", state.externalID, err)
	}
	text := strings.TrimSpace(response.Text)
	if text == "" {
		return "", fmt.Errorf("структура для DEMO external_id %s пуста", state.externalID)
	}
	return text, writeDemoFile(staging, structureFile, text)
}

// article кладёт в DEMO готовую статью, а при её отсутствии генерирует новую. Промпт статьи
// сохраняется всегда — им пользуются в ручном чате, даже когда сама генерация не удалась.
func (b *Builder) article(ctx context.Context, state articleState, staging, structure string) (string, error) {
	call := b.articleCall(state, structure)
	if text, found := b.readProduction(state, state.result.ArticlePath); found {
		if err := writeDemoFile(staging, articleFile, text); err != nil {
			return "", err
		}
		if !b.copyProduction(state, state.production.ArticlePromptPath, staging, articlePromptFile) {
			if err := b.writePrompt(state, staging, articlePromptFile, call); err != nil {
				return text, err
			}
		}
		return text, nil
	}
	if err := b.writePrompt(state, staging, articlePromptFile, call); err != nil {
		return "", err
	}
	response, err := b.generator.Generate(ctx, call)
	if err != nil {
		return "", fmt.Errorf("сгенерировать статью для DEMO external_id %s: %w", state.externalID, err)
	}
	text := strings.TrimSpace(strings.ReplaceAll(response.Text, "[[ARTICLE_COMPLETE]]", ""))
	if text == "" {
		return "", fmt.Errorf("статья для DEMO external_id %s пуста", state.externalID)
	}
	return text, writeDemoFile(staging, articleFile, text)
}

func (b *Builder) articleCall(state articleState, structure string) llm.Call {
	return llm.Call{Stage: "article", ArticleID: state.result.Article.ID, Data: struct {
		Title              string
		Keywords           string
		LSIWords           string
		GeneratedStructure string
	}{
		Title:    state.title(),
		Keywords: article.FormatKeywords(state.research.WordstatKeywords),
		LSIWords: strings.Join(state.research.LSIWords, "\n"), GeneratedStructure: structure,
	}}
}

// articleInfo кладёт в DEMO информацию для публикации и возвращает набор метаданных для
// result.md.
//
// Источник выбирается целиком, а не по полям. Есть метаданные в PostgreSQL — берутся они,
// стадия info не запускается повторно, а сохранённый article_info.txt лишь копируется для
// справки; возвращается nil, и result.md собирается по данным БД. Метаданных в БД нет —
// источником становится article_info.txt: переиспользованный с диска или сгенерированный
// этой же стадией info. Разбирается он целиком, и целиком же уходит в result.md.
func (b *Builder) articleInfo(ctx context.Context, state articleState, staging, structure, articleText string) (*article.ArticleInfo, error) {
	saved, found := b.readProduction(state, state.production.ArticleInfoPath)
	if found {
		if err := writeDemoFile(staging, articleInfoFile, saved); err != nil {
			return nil, err
		}
		b.copyProduction(state, state.production.ArticleInfoPromptPath, staging, articleInfoPromptFile)
	}
	if state.hasStoredMetadata() {
		return nil, nil
	}
	if found {
		return b.parseDemoMetadata(state, saved), nil
	}
	if strings.TrimSpace(articleText) == "" {
		b.logger.Warn("информация для публикации пропущена: статьи нет", "external_id", state.externalID, "stage", "demo_info")
		return nil, nil
	}
	call := llm.Call{Stage: "info", ArticleID: state.result.Article.ID, Data: struct {
		GeneratedStructure string
		GeneratedArticle   string
	}{structure, articleText}}
	if err := b.writePrompt(state, staging, articleInfoPromptFile, call); err != nil {
		return nil, err
	}
	response, err := b.generator.Generate(ctx, call)
	if err != nil {
		return nil, fmt.Errorf("собрать информацию для DEMO external_id %s: %w", state.externalID, err)
	}
	text := strings.TrimSpace(response.Text)
	if text == "" {
		return nil, fmt.Errorf("информация для DEMO external_id %s пуста", state.externalID)
	}
	if err := writeDemoFile(staging, articleInfoFile, text); err != nil {
		return nil, err
	}
	return b.parseDemoMetadata(state, text), nil
}

// parseDemoMetadata разбирает article_info.txt для result.md. Неразобранный ответ не теряется:
// он целиком уходит в TL;DR, а причина остаётся в логе статьи — пустой раздел в result.md
// молча пропускают глазами, а текст не по формату виден сразу.
func (b *Builder) parseDemoMetadata(state articleState, text string) *article.ArticleInfo {
	parsed, err := article.ParseArticleInfo(text)
	if err == nil && (parsed.Tags != "" || parsed.TLDR != "" || parsed.FAQ != "") {
		if parsed.FallbackUsed {
			b.logger.Warn("метаданные DEMO разобраны нестрогим разбором", "external_id", state.externalID,
				"stage", "demo_info", "has_tags", parsed.Tags != "", "has_tldr", parsed.TLDR != "", "has_faq", parsed.FAQ != "")
		}
		return &parsed
	}
	b.logger.Warn("метаданные DEMO не разобраны, ответ стадии info целиком помещён в TL;DR",
		"external_id", state.externalID, "stage", "demo_info", "error", err)
	return &article.ArticleInfo{TLDR: strings.TrimSpace(text)}
}

// mergedPrompt рендерит объединённый промпт fix + перелинковка + HTML. Ревью в него не
// входит: оно остаётся выше в истории ручного чата.
func (b *Builder) mergedPrompt(state articleState, staging string) error {
	templateText, err := os.ReadFile(b.mergedPromptPath)
	if err != nil {
		return fmt.Errorf("прочитать промпт %q: %w", b.mergedPromptPath, err)
	}
	tmpl, err := template.New(fixLinksHTMLPromptFile).Option("missingkey=error").Parse(string(templateText))
	if err != nil {
		return fmt.Errorf("разобрать промпт %q: %w", b.mergedPromptPath, err)
	}
	var rendered strings.Builder
	if err := tmpl.Execute(&rendered, struct{ Professions, Links string }{state.professions(), state.links()}); err != nil {
		return fmt.Errorf("отрендерить промпт %q: %w", b.mergedPromptPath, err)
	}
	return writeDemoFile(staging, fixLinksHTMLPromptFile, rendered.String())
}

// resultMarkdown собирает result.md всегда: недостающие поля остаются пустыми. metadata == nil
// означает, что источником метаданных остаётся PostgreSQL.
func (b *Builder) resultMarkdown(ctx context.Context, state articleState, staging, articleText string, metadata *article.ArticleInfo) error {
	rendered, err := b.result.RenderForDemo(ctx, state.externalID, articleText, metadata)
	if err != nil {
		return fmt.Errorf("собрать result.md для DEMO external_id %s: %w", state.externalID, err)
	}
	return writeDemoFile(staging, resultFile, rendered)
}

// copyPrepare переносит диагностику prepare: без неё по DEMO не понять, из каких исходных
// данных выросла статья. Отсутствие файлов ошибкой не считается.
func (b *Builder) copyPrepare(state articleState, staging string) {
	source := filepath.Join(b.root, state.directory, prepareFolder)
	entries, err := os.ReadDir(source)
	if err != nil {
		if !os.IsNotExist(err) {
			b.logger.Warn("данные prepare не прочитаны", "external_id", state.externalID, "stage", "demo_prepare", "error", err)
		}
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(source, entry.Name()))
		if readErr != nil {
			b.logger.Warn("файл prepare не скопирован", "external_id", state.externalID,
				"stage", "demo_prepare", "file", entry.Name(), "error", readErr)
			continue
		}
		if writeErr := writeDemoFile(staging, filepath.Join(prepareFolder, entry.Name()), string(content)); writeErr != nil {
			b.logger.Warn("файл prepare не записан", "external_id", state.externalID,
				"stage", "demo_prepare", "file", entry.Name(), "error", writeErr)
		}
	}
}

func (b *Builder) writePrompt(state articleState, staging, name string, call llm.Call) error {
	prepared, err := b.generator.Prepare(call)
	if err != nil {
		return fmt.Errorf("собрать промпт стадии %q для DEMO external_id %s: %w", call.Stage, state.externalID, err)
	}
	return writeDemoFile(staging, name, prepared.Prompt)
}

// readProduction читает боевой артефакт статьи. Путь из PostgreSQL проверяется на
// принадлежность каталогу этой же статьи: чужой артефакт молча подмешивать нельзя.
func (b *Builder) readProduction(state articleState, relativePath string) (string, bool) {
	if strings.TrimSpace(relativePath) == "" || !b.artifacts.Exists(relativePath) {
		return "", false
	}
	if !strings.HasPrefix(filepath.ToSlash(relativePath), state.directory+"/") {
		b.logger.Warn("артефакт принадлежит другой статье и пропущен", "external_id", state.externalID,
			"stage", "demo_build", "path", relativePath)
		return "", false
	}
	text, err := b.artifacts.Read(relativePath)
	if err != nil {
		b.logger.Warn("артефакт не прочитан", "external_id", state.externalID,
			"stage", "demo_build", "path", relativePath, "error", err)
		return "", false
	}
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

func (b *Builder) copyProduction(state articleState, relativePath, staging, name string) bool {
	text, found := b.readProduction(state, relativePath)
	if !found {
		return false
	}
	if err := writeDemoFile(staging, name, text); err != nil {
		b.logger.Warn("файл DEMO не записан", "external_id", state.externalID,
			"stage", "demo_build", "file", name, "error", err)
		return false
	}
	return true
}

func writeDemoFile(staging, name, content string) error {
	path := filepath.Join(staging, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("создать каталог для %s: %w", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("записать %s: %w", name, err)
	}
	return nil
}

// Package result assembles result.md from persisted article data.
package result

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

// TemplateFileName — имя шаблона внутри каталога templates задачи. Полный путь приходит
// снаружи: пакет собирает result.md любой задачи и её имени не знает.
const TemplateFileName = "result.md.tmpl"

// FAQItem is one structured question and answer rendered in result.md.
type FAQItem struct {
	Question string
	Answer   string
}

type Repository interface {
	GetResultInput(ctx context.Context, externalID string) (article.ResultInput, error)
}

type Writer interface {
	Read(relativePath string) (string, error)
	Exists(relativePath string) bool
	StageResult(externalID, slug, content string) (*articleoutput.PendingArtifact, error)
}

type Service struct {
	repository   Repository
	writer       Writer
	logger       *slog.Logger
	templatePath string
}

// NewService собирает сборщик result.md. templatePath — обязательная зависимость и потому
// обычный параметр: шаблон у каждой задачи свой.
func NewService(repository Repository, writer Writer, logger *slog.Logger, templatePath string) *Service {
	return &Service{repository: repository, writer: writer, logger: logger, templatePath: templatePath}
}

// ReadingTimeMinutes calculates reading time at 180 words per minute.
func ReadingTimeMinutes(text string) int {
	words := len(strings.Fields(text))
	if words == 0 {
		return 0
	}
	return (words + 179) / 180
}

type templateData struct {
	article.ResultInput
	Title              string
	SEOTitle           string
	ProfessionName     string
	ImageName          string
	ImageURL           string
	ReadingTimeMinutes int
	FAQItems           []FAQItem
}

// ParseFAQItems converts persisted FAQ text into question-answer pairs.
func ParseFAQItems(text string) ([]FAQItem, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil, nil
	}
	var items []FAQItem
	var current *FAQItem
	section := ""
	flush := func() error {
		if current == nil {
			return nil
		}
		current.Question = strings.TrimSpace(current.Question)
		current.Answer = strings.TrimSpace(current.Answer)
		if current.Question == "" || current.Answer == "" {
			return fmt.Errorf("FAQ item must contain both question and answer")
		}
		items = append(items, *current)
		return nil
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Вопрос:"):
			if err := flush(); err != nil {
				return nil, err
			}
			current = &FAQItem{Question: strings.TrimSpace(strings.TrimPrefix(trimmed, "Вопрос:"))}
			section = "question"
		case strings.HasPrefix(trimmed, "Ответ:"):
			if current == nil {
				return nil, fmt.Errorf("FAQ answer appears before question")
			}
			current.Answer = strings.TrimSpace(strings.TrimPrefix(trimmed, "Ответ:"))
			section = "answer"
		case current != nil && trimmed != "":
			if section == "answer" {
				current.Answer = strings.TrimSpace(current.Answer + "\n" + trimmed)
			} else {
				current.Question = strings.TrimSpace(current.Question + "\n" + trimmed)
			}
		case current == nil && trimmed != "":
			return nil, fmt.Errorf("FAQ text must use Вопрос: and Ответ: markers")
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return items, nil
}

// Build loads persisted data and atomically rebuilds result.md without an LLM.
func (s *Service) Build(ctx context.Context, externalID string) (articleoutput.ArticlePaths, error) {
	pending, err := s.BuildStaged(ctx, externalID)
	if err != nil {
		return articleoutput.ArticlePaths{}, err
	}
	defer pending.Abort()
	if err := articleoutput.Commit(nil, pending); err != nil {
		return articleoutput.ArticlePaths{}, err
	}
	s.logger.Info("result saved", "external_id", externalID, "stage", "result_generation", "result_path", pending.Paths.ResultPath)
	return pending.Paths, nil
}

func (s *Service) BuildStaged(ctx context.Context, externalID string) (*articleoutput.PendingArtifact, error) {
	s.logger.Info("result generation started", "external_id", externalID, "stage", "result_generation")
	input, err := s.repository.GetResultInput(ctx, externalID)
	if err != nil {
		return nil, fmt.Errorf("load result data for external_id %s: %w", externalID, err)
	}
	if strings.TrimSpace(input.ArticlePath) == "" {
		return nil, fmt.Errorf("article_path is missing for external_id %s", externalID)
	}
	articleText, err := s.writer.Read(input.ArticlePath)
	if err != nil {
		return nil, fmt.Errorf("read article file %q: %w", input.ArticlePath, err)
	}
	if input.HTMLPath != "" && !s.writer.Exists(input.HTMLPath) {
		input.HTMLPath = ""
	}
	s.warnMissingResultFields(input)
	faqItems, err := ParseFAQItems(input.FAQ)
	if err != nil {
		return nil, fmt.Errorf("parse FAQ for result: %w", err)
	}
	rendered, err := s.render(input, articleText, faqItems)
	if err != nil {
		return nil, err
	}
	outputSlug := input.Article.Slug
	if strings.TrimSpace(outputSlug) == "" {
		articleDirectory := strings.Split(strings.Trim(filepath.ToSlash(input.ArticlePath), "/"), "/")[0]
		outputSlug = strings.TrimPrefix(articleDirectory, input.Article.ExternalID+"-")
	}
	pending, err := s.writer.StageResult(input.Article.ExternalID, outputSlug, rendered)
	if err != nil {
		return nil, err
	}
	s.logger.Info("result staged", "article_id", input.Article.ID, "external_id", externalID, "stage", "result_generation", "result_path", pending.Paths.ResultPath)
	return pending, nil
}

// render fills result.md.tmpl. Единственное место, где шаблон читается и исполняется:
// боевая сборка и demo обязаны давать один и тот же файл из одних и тех же данных.
func (s *Service) render(input article.ResultInput, articleText string, faqItems []FAQItem) (string, error) {
	templateText, err := os.ReadFile(s.templatePath)
	if err != nil {
		return "", fmt.Errorf("read result template %q: %w", s.templatePath, err)
	}
	tmpl, err := template.New("result.md").Funcs(template.FuncMap{
		"add": func(left, right int) int { return left + right },
	}).Option("missingkey=error").Parse(string(templateText))
	if err != nil {
		return "", fmt.Errorf("parse result template %q: %w", s.templatePath, err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, templateData{
		ResultInput:        input,
		Title:              input.Article.Title,
		SEOTitle:           input.Article.Title,
		ProfessionName:     input.Keyword,
		ImageName:          input.Header,
		ImageURL:           input.Article.Slug,
		ReadingTimeMinutes: ReadingTimeMinutes(articleText),
		FAQItems:           faqItems,
	}); err != nil {
		return "", fmt.Errorf("render result template: %w", err)
	}
	return rendered.String(), nil
}

// RenderForDemo renders result.md from whatever is already persisted, without writing anything.
// В отличие от Build отсутствующие данные здесь не ошибка: demo обязан получить result.md и на
// статье, которая ещё не прошла пайплайн, — незаполненные поля остаются пустыми.
//
// metadata подменяет сохранённые TL;DR, FAQ и допинфо целиком — пополевого смешивания с
// PostgreSQL нет: набор метаданных приходит либо из БД (metadata == nil), либо из разобранного
// demo article_info.txt. Половина одного источника с половиной другого дала бы result.md,
// которого не существует ни в одном прогоне.
func (s *Service) RenderForDemo(ctx context.Context, externalID, articleText string, metadata *article.ArticleInfo) (string, error) {
	input, err := s.repository.GetResultInput(ctx, externalID)
	if err != nil {
		return "", fmt.Errorf("load result data for external_id %s: %w", externalID, err)
	}
	if input.HTMLPath != "" && !s.writer.Exists(input.HTMLPath) {
		input.HTMLPath = ""
	}
	// Метки сюда не попадают намеренно: они приходят из article_inputs — колонки tags
	// Excel — и переданный набор метаданных их не подменяет.
	if metadata != nil {
		input.TLDR, input.FAQ, input.AdditionalInfo = metadata.TLDR, metadata.FAQ, metadata.AdditionalInfo
	}
	faqItems, err := ParseFAQItems(input.FAQ)
	if err != nil {
		s.logger.Warn("FAQ не разобран, раздел вопросов останется пустым",
			"external_id", externalID, "stage", "result_generation", "error", err)
		faqItems = nil
	}
	return s.render(input, articleText, faqItems)
}

func (s *Service) warnMissingResultFields(input article.ResultInput) {
	fields := []struct {
		name  string
		value string
	}{
		{"SEO-заголовок", input.Article.Title},
		{"Название профессии", input.Keyword},
		{"Название картинки", input.Header},
		{"URL картинки", input.Article.Slug},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			s.logger.Warn("result field is empty", "article_id", input.Article.ID, "external_id", input.Article.ExternalID, "field", field.name)
		}
	}
}

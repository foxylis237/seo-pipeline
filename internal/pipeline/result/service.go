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

// TemplateFileName — имя шаблона внутри каталога templates задачи.
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
	// courses — подбор связанных курсов; nil у задачи без блока.
	courses CourseSelector
}

// NewService собирает сборщик result.md по шаблону задачи.
func NewService(repository Repository, writer Writer, logger *slog.Logger, templatePath string) *Service {
	return &Service{repository: repository, writer: writer, logger: logger, templatePath: templatePath}
}

// RelatedCourse — одна карточка блока «Связанные курсы» под статьёй (свой тип, а не каталога).
type RelatedCourse struct {
	Name     string
	Category string
	URL      string
	// Neighbour — курс смежной профессии, а не той, о которой статья.
	Neighbour bool
}

// CourseSelector подбирает курсы под уже собранные данные статьи.
type CourseSelector interface {
	RelatedCourses(ctx context.Context, input article.ResultInput) ([]RelatedCourse, error)
}

// UseCourseSelector подключает подбор связанных курсов.
func (s *Service) UseCourseSelector(courses CourseSelector) {
	s.courses = courses
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
	// RelatedCourses — блок связанных курсов; пуст без блока и при неудачном подборе.
	RelatedCourses []RelatedCourse
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
	return pending.Paths, nil
}

func (s *Service) BuildStaged(ctx context.Context, externalID string) (*articleoutput.PendingArtifact, error) {
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
	rendered, err := s.render(input, articleText, faqItems, s.relatedCourses(ctx, input))
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
	s.logger.Info("result.md собран", "article_id", input.Article.ID, "external_id", externalID, "stage", "result", "path", pending.Paths.ResultPath)
	return pending, nil
}

// relatedCourses подбирает блок под статьёй; отказ подбора лист не роняет.
func (s *Service) relatedCourses(ctx context.Context, input article.ResultInput) []RelatedCourse {
	if s.courses == nil {
		return nil
	}
	courses, err := s.courses.RelatedCourses(ctx, input)
	if err != nil {
		s.logger.Warn("связанные курсы не подобраны", "external_id", input.Article.ExternalID,
			"stage", "result", "error", err)
		return nil
	}
	return courses
}

// render fills result.md.tmpl — общий для боевой сборки и demo.
func (s *Service) render(
	input article.ResultInput, articleText string, faqItems []FAQItem, courses []RelatedCourse,
) (string, error) {
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
		ResultInput: input,
		Title:       input.Article.Title,
		// Без своих колонок — заголовок статьи и фокусное ключевое слово.
		SEOTitle:           fallback(input.SEOTitle, input.Article.Title),
		ProfessionName:     fallback(input.Profession, input.Keyword),
		ImageName:          input.Header,
		ImageURL:           input.Article.Slug,
		ReadingTimeMinutes: ReadingTimeMinutes(articleText),
		FAQItems:           faqItems,
		RelatedCourses:     courses,
	}); err != nil {
		return "", fmt.Errorf("render result template: %w", err)
	}
	return rendered.String(), nil
}

// RenderForDemo renders result.md from whatever is already persisted, without writing anything;
// missing data is not an error. Non-nil metadata replaces the stored set entirely, not per field.
func (s *Service) RenderForDemo(ctx context.Context, externalID, articleText string, metadata *article.ArticleInfo) (string, error) {
	input, err := s.repository.GetResultInput(ctx, externalID)
	if err != nil {
		return "", fmt.Errorf("load result data for external_id %s: %w", externalID, err)
	}
	if input.HTMLPath != "" && !s.writer.Exists(input.HTMLPath) {
		input.HTMLPath = ""
	}
	// Метки берутся из article_inputs, metadata их не подменяет.
	if metadata != nil {
		input.TLDR, input.FAQ, input.AdditionalInfo = metadata.TLDR, metadata.FAQ, metadata.AdditionalInfo
	}
	faqItems, err := ParseFAQItems(input.FAQ)
	if err != nil {
		s.logger.Warn("FAQ не разобран, раздел вопросов останется пустым",
			"external_id", externalID, "stage", "result", "error", err)
		faqItems = nil
	}
	// Тот же подбор курсов, что у боевого листа.
	return s.render(input, articleText, faqItems, s.relatedCourses(ctx, input))
}

// fallback возвращает value, а пустое — previous.
func fallback(value, previous string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return previous
}

func (s *Service) warnMissingResultFields(input article.ResultInput) {
	fields := []struct {
		name  string
		value string
	}{
		{"SEO-заголовок", fallback(input.SEOTitle, input.Article.Title)},
		{"Название профессии", fallback(input.Profession, input.Keyword)},
		{"Название картинки", input.Header},
		{"URL картинки", input.Article.Slug},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			s.logger.Warn("поле result.md пустое", "article_id", input.Article.ID, "external_id", input.Article.ExternalID, "stage", "result", "field", field.name)
		}
	}
}

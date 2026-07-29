// Package output writes generated article artifacts to the filesystem.
package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ArticlePaths contains paths relative to the configured output root.
type ArticlePaths struct {
	StructurePromptPath   string
	StructurePath         string
	ArticlePromptPath     string
	ArticlePath           string
	GenerationInfoPath    string
	ArticleInfoPromptPath string
	ArticleInfoPath       string
	ReviewPromptPath      string
	ReviewPath            string
	FixPromptPath         string
	FixedArticlePath      string
	HTMLPromptPath        string
	HTMLPath              string
}

// SaveArticleInfo writes or replaces the rendered info prompt and publication information.
func (w *Writer) SaveArticleInfo(externalID, slug, prompt, info string) (ArticlePaths, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article generated directory: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.ArticleInfoPromptPath)), []byte(prompt)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article info prompt: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.ArticleInfoPath)), []byte(info)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article info: %w", err)
	}
	return paths, nil
}

// SaveFixedArticle writes the fix prompt and corrected article.
func (w *Writer) SaveFixedArticle(externalID, slug, prompt, article string) (ArticlePaths, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article generated directory: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.FixPromptPath)), []byte(prompt)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article fix prompt: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.FixedArticlePath)), []byte(article)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write fixed article: %w", err)
	}
	return paths, nil
}

// SaveReview writes the review prompt and full model response.
func (w *Writer) SaveReview(externalID, slug, prompt, review string) (ArticlePaths, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article generated directory: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.ReviewPromptPath)), []byte(prompt)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article review prompt: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.ReviewPath)), []byte(review)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article review: %w", err)
	}
	return paths, nil
}

// SaveHTML writes the HTML prompt and validated final HTML.
func (w *Writer) SaveHTML(externalID, slug, prompt, html string) (ArticlePaths, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.HTMLPromptPath)), []byte(prompt)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article HTML prompt: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.HTMLPath)), []byte(html)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article HTML: %w", err)
	}
	return paths, nil
}

// Writer saves article artifacts under one output root.
type Writer struct {
	root string
}

func NewWriter(root string) *Writer {
	return &Writer{root: root}
}

// Read reads an artifact by a path previously returned by Writer.
func (w *Writer) Read(relativePath string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(relativePath))
	if relativePath == "" || filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid relative output path %q", relativePath)
	}
	data, err := os.ReadFile(filepath.Join(w.root, cleaned))
	if err != nil {
		return "", fmt.Errorf("read output file %s: %w", relativePath, err)
	}
	return string(data), nil
}

func (w *Writer) SaveStructure(externalID, slug, prompt, structure string) (ArticlePaths, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article generated directory: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.StructurePromptPath)), []byte(prompt)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write structure prompt: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.StructurePath)), []byte(structure)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write generated structure: %w", err)
	}
	return paths, nil
}

// SaveArticle writes or replaces the article prompt and generated text as UTF-8.
func (w *Writer) SaveArticle(externalID, slug, prompt, text, model string) (ArticlePaths, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return ArticlePaths{}, fmt.Errorf("create article generated directory: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.ArticlePromptPath)), []byte(prompt)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article prompt: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.ArticlePath)), []byte(text)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article: %w", err)
	}
	info, err := json.MarshalIndent(struct {
		ExternalID        string `json:"external_id"`
		Model             string `json:"model"`
		ArticlePromptPath string `json:"article_prompt_path"`
		ArticlePath       string `json:"article_path"`
	}{externalID, model, paths.ArticlePromptPath, paths.ArticlePath}, "", "  ")
	if err != nil {
		return ArticlePaths{}, fmt.Errorf("encode generation context: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(w.root, filepath.FromSlash(paths.GenerationInfoPath)), info); err != nil {
		return ArticlePaths{}, fmt.Errorf("write generation context: %w", err)
	}
	return paths, nil
}

func (w *Writer) articlePaths(externalID, slug string) (ArticlePaths, string, error) {
	if err := validatePathPart("external ID", externalID); err != nil {
		return ArticlePaths{}, "", err
	}
	if err := validatePathPart("slug", slug); err != nil {
		return ArticlePaths{}, "", err
	}
	directoryName := externalID + "-" + slug
	articleDirectory := filepath.Join(w.root, directoryName)
	return ArticlePaths{
		StructurePromptPath:   filepath.ToSlash(filepath.Join(directoryName, "prompts", "structure_prompt.txt")),
		StructurePath:         filepath.ToSlash(filepath.Join(directoryName, "generated", "structure.txt")),
		ArticlePromptPath:     filepath.ToSlash(filepath.Join(directoryName, "prompts", "article_prompt.txt")),
		ArticlePath:           filepath.ToSlash(filepath.Join(directoryName, "generated", "article.txt")),
		GenerationInfoPath:    filepath.ToSlash(filepath.Join(directoryName, "generated", "generation_context.json")),
		ArticleInfoPromptPath: filepath.ToSlash(filepath.Join(directoryName, "prompts", "article_info_prompt.txt")),
		ArticleInfoPath:       filepath.ToSlash(filepath.Join(directoryName, "generated", "article_info.txt")),
		ReviewPromptPath:      filepath.ToSlash(filepath.Join(directoryName, "prompts", "article_review_prompt.txt")),
		ReviewPath:            filepath.ToSlash(filepath.Join(directoryName, "generated", "review.txt")),
		FixPromptPath:         filepath.ToSlash(filepath.Join(directoryName, "prompts", "fix_article_prompt.txt")),
		FixedArticlePath:      filepath.ToSlash(filepath.Join(directoryName, "generated", "fixed_article.txt")),
		HTMLPromptPath:        filepath.ToSlash(filepath.Join(directoryName, "prompts", "article_html_prompt.txt")),
		HTMLPath:              filepath.ToSlash(filepath.Join(directoryName, "article.html")),
	}, articleDirectory, nil
}

func validatePathPart(name, value string) error {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("invalid %s %q", name, value)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) (returnErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

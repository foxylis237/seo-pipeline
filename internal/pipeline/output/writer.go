// Package output writes generated article artifacts to the filesystem.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	ResultPath            string
}

type stagedFile struct {
	temporaryPath string
	finalPath     string
	backupPath    string
	published     bool
}

// PendingArtifact contains fully written and closed temporary files awaiting publication.
type PendingArtifact struct {
	Paths  ArticlePaths
	files  []stagedFile
	rename func(string, string) error
	done   bool
}

// Abort removes unpublished temporary files.
func (p *PendingArtifact) Abort() {
	if p == nil || p.done {
		return
	}
	p.cleanup()
	p.done = true
}

// Commit publishes all staged files and persists their state, restoring old files on error.
func Commit(persist func() error, artifacts ...*PendingArtifact) error {
	completed := false
	defer func() {
		if completed {
			return
		}
		for _, artifact := range artifacts {
			if artifact == nil {
				continue
			}
			artifact.cleanup()
			artifact.done = true
		}
	}()
	var files []*stagedFile
	for _, artifact := range artifacts {
		if artifact == nil || artifact.done {
			return fmt.Errorf("artifact is not pending")
		}
		for index := range artifact.files {
			files = append(files, &artifact.files[index])
		}
	}
	rollback := func() error {
		var rollbackErr error
		for index := len(files) - 1; index >= 0; index-- {
			file := files[index]
			if file.published {
				if err := os.Remove(file.finalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove unpublished artifact: %w", err))
				}
			}
			if file.backupPath != "" {
				if !file.published {
					if err := os.Remove(file.backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
						rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove unused artifact backup: %w", err))
					}
					file.backupPath = ""
					continue
				}
				if err := os.Rename(file.backupPath, file.finalPath); err != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore previous artifact from %s: %w", file.backupPath, err))
					file.backupPath = ""
				} else {
					file.backupPath = ""
				}
			}
		}
		return rollbackErr
	}
	for _, artifact := range artifacts {
		for index := range artifact.files {
			file := &artifact.files[index]
			if _, err := os.Stat(file.finalPath); err == nil {
				backup, err := reservePath(filepath.Dir(file.finalPath), ".backup-*")
				if err != nil {
					return errors.Join(err, rollback())
				}
				if err := os.Link(file.finalPath, backup); err != nil {
					_ = os.Remove(backup)
					return errors.Join(fmt.Errorf("preserve existing artifact: %w", err), rollback())
				}
				file.backupPath = backup
			} else if !errors.Is(err, os.ErrNotExist) {
				return errors.Join(fmt.Errorf("inspect existing artifact: %w", err), rollback())
			}
			if err := artifact.rename(file.temporaryPath, file.finalPath); err != nil {
				return errors.Join(fmt.Errorf("publish artifact: %w", err), rollback())
			}
			file.published = true
		}
	}
	if persist != nil {
		if err := persist(); err != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
			return err
		}
	}
	for _, artifact := range artifacts {
		artifact.cleanup()
		artifact.done = true
	}
	completed = true
	return nil
}

// SaveResult atomically writes or replaces the final text summary.
func (w *Writer) SaveResult(externalID, slug, content string) (ArticlePaths, error) {
	pending, err := w.StageResult(externalID, slug, content)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := Commit(nil, pending); err != nil {
		return ArticlePaths{}, err
	}
	return pending.Paths, nil
}

func (w *Writer) StageResult(externalID, slug, content string) (*PendingArtifact, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(articleDirectory, 0o755); err != nil {
		return nil, fmt.Errorf("create article directory: %w", err)
	}
	pending, err := w.stage(paths, []fileContent{{paths.ResultPath, []byte(content)}})
	if err != nil {
		return nil, fmt.Errorf("write result: %w", err)
	}
	return pending, nil
}

// Exists reports whether a saved relative output path points to a regular file.
func (w *Writer) Exists(relativePath string) bool {
	cleaned := filepath.Clean(filepath.FromSlash(relativePath))
	if relativePath == "" || filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return false
	}
	info, err := os.Stat(filepath.Join(w.root, cleaned))
	return err == nil && info.Mode().IsRegular()
}

// SaveArticleInfo writes or replaces the rendered info prompt and publication information.
func (w *Writer) SaveArticleInfo(externalID, slug, prompt, info string) (ArticlePaths, error) {
	pending, err := w.StageArticleInfo(externalID, slug, prompt, info)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := Commit(nil, pending); err != nil {
		return ArticlePaths{}, err
	}
	return pending.Paths, nil
}

func (w *Writer) StageArticleInfo(externalID, slug, prompt, info string) (*PendingArtifact, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return nil, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return nil, fmt.Errorf("create article generated directory: %w", err)
	}
	pending, err := w.stage(paths, []fileContent{{paths.ArticleInfoPromptPath, []byte(prompt)}, {paths.ArticleInfoPath, []byte(info)}})
	if err != nil {
		return nil, fmt.Errorf("stage article info: %w", err)
	}
	return pending, nil
}

// SaveFixedArticle writes the fix prompt and corrected article.
func (w *Writer) SaveFixedArticle(externalID, slug, prompt, article string) (ArticlePaths, error) {
	pending, err := w.StageFixedArticle(externalID, slug, prompt, article)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := Commit(nil, pending); err != nil {
		return ArticlePaths{}, err
	}
	return pending.Paths, nil
}

func (w *Writer) StageFixedArticle(externalID, slug, prompt, article string) (*PendingArtifact, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return nil, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return nil, fmt.Errorf("create article generated directory: %w", err)
	}
	pending, err := w.stage(paths, []fileContent{{paths.FixPromptPath, []byte(prompt)}, {paths.FixedArticlePath, []byte(article)}})
	if err != nil {
		return nil, fmt.Errorf("stage fixed article: %w", err)
	}
	return pending, nil
}

// SaveReview writes the review prompt and full model response.
func (w *Writer) SaveReview(externalID, slug, prompt, review string) (ArticlePaths, error) {
	pending, err := w.StageReview(externalID, slug, prompt, review)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := Commit(nil, pending); err != nil {
		return ArticlePaths{}, err
	}
	return pending.Paths, nil
}

func (w *Writer) StageReview(externalID, slug, prompt, review string) (*PendingArtifact, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return nil, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return nil, fmt.Errorf("create article generated directory: %w", err)
	}
	pending, err := w.stage(paths, []fileContent{{paths.ReviewPromptPath, []byte(prompt)}, {paths.ReviewPath, []byte(review)}})
	if err != nil {
		return nil, fmt.Errorf("stage article review: %w", err)
	}
	return pending, nil
}

// SaveHTML writes the HTML prompt and validated final HTML.
func (w *Writer) SaveHTML(externalID, slug, prompt, html string) (ArticlePaths, error) {
	pending, err := w.StageHTML(externalID, slug, prompt, html)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := Commit(nil, pending); err != nil {
		return ArticlePaths{}, err
	}
	return pending.Paths, nil
}

func (w *Writer) StageHTML(externalID, slug, prompt, html string) (*PendingArtifact, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return nil, fmt.Errorf("create article prompt directory: %w", err)
	}
	pending, err := w.stage(paths, []fileContent{{paths.HTMLPromptPath, []byte(prompt)}, {paths.HTMLPath, []byte(html)}})
	if err != nil {
		return nil, fmt.Errorf("stage article HTML: %w", err)
	}
	return pending, nil
}

// Writer saves article artifacts under one output root.
type Writer struct {
	root   string
	rename func(string, string) error
	write  func(*os.File, []byte) (int, error)
}

func NewWriter(root string) *Writer {
	return &Writer{root: root, rename: os.Rename, write: func(file *os.File, data []byte) (int, error) {
		return file.Write(data)
	}}
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
	pending, err := w.StageStructure(externalID, slug, prompt, structure)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := Commit(nil, pending); err != nil {
		return ArticlePaths{}, err
	}
	return pending.Paths, nil
}

func (w *Writer) StageStructure(externalID, slug, prompt, structure string) (*PendingArtifact, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return nil, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return nil, fmt.Errorf("create article generated directory: %w", err)
	}
	pending, err := w.stage(paths, []fileContent{{paths.StructurePromptPath, []byte(prompt)}, {paths.StructurePath, []byte(structure)}})
	if err != nil {
		return nil, fmt.Errorf("stage generated structure: %w", err)
	}
	return pending, nil
}

// SaveArticle writes or replaces the article prompt and generated text as UTF-8.
func (w *Writer) SaveArticle(externalID, slug, prompt, text, model string) (ArticlePaths, error) {
	pending, err := w.StageArticle(externalID, slug, prompt, text, model)
	if err != nil {
		return ArticlePaths{}, err
	}
	if err := Commit(nil, pending); err != nil {
		return ArticlePaths{}, err
	}
	return pending.Paths, nil
}

func (w *Writer) StageArticle(externalID, slug, prompt, text, model string) (*PendingArtifact, error) {
	paths, articleDirectory, err := w.articlePaths(externalID, slug)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "prompts"), 0o755); err != nil {
		return nil, fmt.Errorf("create article prompt directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		return nil, fmt.Errorf("create article generated directory: %w", err)
	}
	info, err := json.MarshalIndent(struct {
		ExternalID        string `json:"external_id"`
		Model             string `json:"model"`
		ArticlePromptPath string `json:"article_prompt_path"`
		ArticlePath       string `json:"article_path"`
	}{externalID, model, paths.ArticlePromptPath, paths.ArticlePath}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode generation context: %w", err)
	}
	pending, err := w.stage(paths, []fileContent{
		{paths.ArticlePromptPath, []byte(prompt)}, {paths.ArticlePath, []byte(text)}, {paths.GenerationInfoPath, info},
	})
	if err != nil {
		return nil, fmt.Errorf("stage article: %w", err)
	}
	return pending, nil
}

func (w *Writer) articlePaths(externalID, slug string) (ArticlePaths, string, error) {
	paths, err := Paths(externalID, slug)
	if err != nil {
		return ArticlePaths{}, "", err
	}
	return paths, filepath.Join(w.root, DirectoryName(externalID, slug)), nil
}

// DirectoryName is the per-article directory relative to the output root.
func DirectoryName(externalID, slug string) string {
	return externalID + "-" + slug
}

// Paths returns the artifact paths of one article relative to the output root. Exported so
// that assembling a folder from already produced artifacts does not restate the layout.
func Paths(externalID, slug string) (ArticlePaths, error) {
	if err := validatePathPart("external ID", externalID); err != nil {
		return ArticlePaths{}, err
	}
	if err := validatePathPart("slug", slug); err != nil {
		return ArticlePaths{}, err
	}
	directoryName := DirectoryName(externalID, slug)
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
		ResultPath:            filepath.ToSlash(filepath.Join(directoryName, "result.md")),
	}, nil
}

func validatePathPart(name, value string) error {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("invalid %s %q", name, value)
	}
	return nil
}

type fileContent struct {
	relativePath string
	data         []byte
}

func (w *Writer) stage(paths ArticlePaths, contents []fileContent) (*PendingArtifact, error) {
	pending := &PendingArtifact{Paths: paths, rename: w.rename}
	for _, content := range contents {
		finalPath := filepath.Join(w.root, filepath.FromSlash(content.relativePath))
		temporary, err := os.CreateTemp(filepath.Dir(finalPath), ".tmp-*")
		if err != nil {
			pending.Abort()
			return nil, err
		}
		temporaryPath := temporary.Name()
		pending.files = append(pending.files, stagedFile{temporaryPath: temporaryPath, finalPath: finalPath})
		if err := temporary.Chmod(0o644); err != nil {
			_ = temporary.Close()
			pending.Abort()
			return nil, err
		}
		written, err := w.write(temporary, content.data)
		if err == nil && written != len(content.data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			_ = temporary.Close()
			pending.Abort()
			return nil, err
		}
		if err := temporary.Sync(); err != nil {
			_ = temporary.Close()
			pending.Abort()
			return nil, err
		}
		if err := temporary.Close(); err != nil {
			pending.Abort()
			return nil, err
		}
	}
	return pending, nil
}

func (p *PendingArtifact) cleanup() {
	for index := range p.files {
		_ = os.Remove(p.files[index].temporaryPath)
		_ = os.Remove(p.files[index].backupPath)
	}
}

func reservePath(directory, pattern string) (string, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

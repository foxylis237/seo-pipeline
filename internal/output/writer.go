// Package output writes generated article artifacts to the filesystem.
package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ArticlePaths contains paths relative to the configured output root.
type ArticlePaths struct {
	StructurePromptPath string
	StructurePath       string
	ArticlePromptPath   string
	ArticlePath         string
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

// ResetArticle removes every previous intermediate and final artifact of an article.
func (w *Writer) ResetArticle(externalID, slug string) error {
	if err := validatePathPart("external ID", externalID); err != nil {
		return err
	}
	if err := validatePathPart("slug", slug); err != nil {
		return err
	}
	articleDirectory := filepath.Join(w.root, externalID+"-"+slug)
	if err := os.RemoveAll(articleDirectory); err != nil {
		return fmt.Errorf("remove previous article output directory: %w", err)
	}
	return nil
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
	if err := writeNewFile(filepath.Join(w.root, filepath.FromSlash(paths.StructurePromptPath)), []byte(prompt)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write structure prompt: %w", err)
	}
	if err := writeNewFile(filepath.Join(w.root, filepath.FromSlash(paths.StructurePath)), []byte(structure)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write generated structure: %w", err)
	}
	return paths, nil
}

// SaveArticle writes the article prompt and generated text as UTF-8 without overwriting files.
func (w *Writer) SaveArticle(externalID, slug, prompt, text string) (ArticlePaths, error) {
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
	if err := writeNewFile(filepath.Join(w.root, filepath.FromSlash(paths.ArticlePromptPath)), []byte(prompt)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article prompt: %w", err)
	}
	if err := writeNewFile(filepath.Join(w.root, filepath.FromSlash(paths.ArticlePath)), []byte(text)); err != nil {
		return ArticlePaths{}, fmt.Errorf("write article: %w", err)
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
		StructurePromptPath: filepath.ToSlash(filepath.Join(directoryName, "prompts", "structure_prompt.txt")),
		StructurePath:       filepath.ToSlash(filepath.Join(directoryName, "generated", "structure.txt")),
		ArticlePromptPath:   filepath.ToSlash(filepath.Join(directoryName, "prompts", "article_prompt.txt")),
		ArticlePath:         filepath.ToSlash(filepath.Join(directoryName, "generated", "article.txt")),
	}, articleDirectory, nil
}

func writeNewFile(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("output file already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect output file %s: %w", path, err)
	}
	return writeFileAtomic(path, data)
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
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("file already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporaryPath, path)
}

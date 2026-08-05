package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Subdirectories of one article directory that hold diagnostics rather than article content.
const (
	PrepareSubdirectory = "prepare"
	LogsSubdirectory    = "logs"
)

// Root returns the configured output root. Диагностика пишет свои файлы сама,
// но раскладку каталогов статьи задаёт только Writer.
func (w *Writer) Root() string { return w.root }

// ArticleDirectory returns the article directory name relative to the output root.
func (w *Writer) ArticleDirectory(externalID, slug string) (string, error) {
	if err := validatePathPart("external ID", externalID); err != nil {
		return "", err
	}
	if err := validatePathPart("slug", slug); err != nil {
		return "", err
	}
	return externalID + "-" + slug, nil
}

// resolveArticleDirectory returns the article directory, looking it up on disk when the
// caller does not know the slug.
func (w *Writer) resolveArticleDirectory(externalID, slug string) (string, error) {
	if strings.TrimSpace(slug) != "" {
		return w.ArticleDirectory(externalID, slug)
	}
	if err := validatePathPart("external ID", externalID); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return "", fmt.Errorf("прочитать каталог статей %s: %w", w.root, err)
	}
	prefix := externalID + "-"
	matches := make([]string, 0, 1)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			matches = append(matches, entry.Name())
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("каталог статьи external_id %q не найден в %s", externalID, w.root)
	default:
		// Два каталога на один external_id — обычно из-за смены slug. Гадать нельзя:
		// запись в чужой каталог и есть та самая путаница между статьями.
		return "", fmt.Errorf("для external_id %q найдено несколько каталогов статьи: %s", externalID, strings.Join(matches, ", "))
	}
}

// SaveDiagnostics atomically writes one JSON diagnostics file into a subdirectory of the
// article directory and returns its path relative to the output root. Adding a new
// diagnostics file needs no change here: it is just another name.
func (w *Writer) SaveDiagnostics(externalID, slug, subdirectory, name string, payload any) (string, error) {
	directory, err := w.ArticleDirectory(externalID, slug)
	if err != nil {
		return "", err
	}
	if err := validatePathPart("subdirectory", subdirectory); err != nil {
		return "", err
	}
	if err := validatePathPart("diagnostics file", name); err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("закодировать диагностику %s: %w", name, err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Join(w.root, directory, subdirectory), 0o755); err != nil {
		return "", fmt.Errorf("создать каталог диагностики: %w", err)
	}
	relativePath := filepath.ToSlash(filepath.Join(directory, subdirectory, name))
	pending, err := w.stage(ArticlePaths{}, []fileContent{{relativePath, encoded}})
	if err != nil {
		return "", fmt.Errorf("подготовить диагностику %s: %w", name, err)
	}
	if err := Commit(nil, pending); err != nil {
		return "", fmt.Errorf("опубликовать диагностику %s: %w", name, err)
	}
	return relativePath, nil
}

// ResetDiagnostics removes the diagnostics of the previous run from one subdirectory of the
// article directory. Только этот подкаталог: логи, промпты и сгенерированные файлы статьи
// живут рядом и переживают перезапуск.
//
// Иначе неуспешный прогон оставляет файлы предыдущего, и при разборе они выглядят как
// свежие — именно на этом легко ошибиться в диагнозе.
func (w *Writer) ResetDiagnostics(externalID, slug, subdirectory string) error {
	directory, err := w.ArticleDirectory(externalID, slug)
	if err != nil {
		return err
	}
	if err := validatePathPart("subdirectory", subdirectory); err != nil {
		return err
	}
	target := filepath.Join(w.root, directory, subdirectory)
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("очистить диагностику предыдущего прогона %s: %w", target, err)
	}
	return nil
}

// OpenArticleLog opens the append-only stage log of one article, creating the directory.
// An empty slug means "resolve the existing article directory by external_id", which is what
// every stage after prepare needs: the directory is already there with the artifacts in it.
// The caller owns the returned file and must close it.
func (w *Writer) OpenArticleLog(externalID, slug, name string) (*os.File, string, error) {
	directory, err := w.resolveArticleDirectory(externalID, slug)
	if err != nil {
		return nil, "", err
	}
	if err := validatePathPart("log file", name); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Join(w.root, directory, LogsSubdirectory), 0o755); err != nil {
		return nil, "", fmt.Errorf("создать каталог логов: %w", err)
	}
	relativePath := filepath.ToSlash(filepath.Join(directory, LogsSubdirectory, name))
	file, err := os.OpenFile(filepath.Join(w.root, filepath.FromSlash(relativePath)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, "", fmt.Errorf("открыть лог %s: %w", relativePath, err)
	}
	return file, relativePath, nil
}

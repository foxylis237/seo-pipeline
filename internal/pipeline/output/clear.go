package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// clearKeepsSubdirectories перечисляет то, что переживает очистку статьи; пуст — логи
// очищенной статьи с status=completed были бы неправдой.
var clearKeepsSubdirectories = []string{}

// findArticleDirectoryForClear ищет каталог статьи по external_id; в отличие от
// resolveArticleDirectory отсутствие каталога не ошибка, а несколько каталогов — ошибка.
func (w *Writer) findArticleDirectoryForClear(externalID string) (string, bool, error) {
	if err := validatePathPart("external ID", externalID); err != nil {
		return "", false, err
	}
	entries, err := os.ReadDir(w.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("прочитать каталог статей %s: %w", w.root, err)
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
		return matches[0], true, nil
	case 0:
		return "", false, nil
	default:
		return "", false, fmt.Errorf(
			"для external_id %q найдено несколько каталогов статьи: %s", externalID, strings.Join(matches, ", "))
	}
}

// clearableEntries возвращает элементы верхнего уровня каталога статьи, кроме сохраняемых.
func clearableEntries(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("прочитать каталог статьи %s: %w", root, err)
	}
	kept := make(map[string]struct{}, len(clearKeepsSubdirectories))
	for _, name := range clearKeepsSubdirectories {
		kept[name] = struct{}{}
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, keep := kept[entry.Name()]; keep {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// ClearArticleArtifacts удаляет всё содержимое каталога статьи и сам опустевший каталог,
// возвращая удалённые пути относительно корня вывода; отсутствие каталога — не ошибка.
// Удаляется всё, а не список артефактов: список отставал бы от новых этапов.
func (w *Writer) ClearArticleArtifacts(externalID string) ([]string, error) {
	directory, found, err := w.findArticleDirectoryForClear(externalID)
	if err != nil || !found {
		return nil, err
	}

	root := filepath.Join(w.root, directory)
	names, err := clearableEntries(root)
	if err != nil {
		return nil, err
	}

	removed := make([]string, 0, len(names))
	var failures []error
	for _, name := range names {
		relativePath := filepath.ToSlash(filepath.Join(directory, name))
		if removeErr := os.RemoveAll(filepath.Join(root, name)); removeErr != nil {
			failures = append(failures, fmt.Errorf("удалить %s: %w", relativePath, removeErr))
			continue
		}
		removed = append(removed, relativePath)
	}

	// os.Remove, а не RemoveAll: на непустом откажет и не унесёт уцелевшее после сбоя.
	if len(clearKeepsSubdirectories) == 0 && len(failures) == 0 {
		if removeErr := os.Remove(root); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("удалить каталог статьи %s: %w", directory, removeErr))
		}
	}

	// Удалённое возвращается и вместе с ошибкой: часть файлов уже исчезла.
	return removed, errors.Join(failures...)
}

// CountArticleArtifacts считает, сколько элементов удалит ClearArticleArtifacts, и возвращает
// каталог статьи; пустое имя — каталога ещё нет.
func (w *Writer) CountArticleArtifacts(externalID string) (string, int, error) {
	directory, found, err := w.findArticleDirectoryForClear(externalID)
	if err != nil || !found {
		return "", 0, err
	}
	names, err := clearableEntries(filepath.Join(w.root, directory))
	if err != nil {
		return "", 0, err
	}
	return directory, len(names), nil
}

// ClearKeepsDescription перечисляет сохраняемые подкаталоги для отчёта; пустая строка — не сохраняется ничего.
func ClearKeepsDescription() string {
	if len(clearKeepsSubdirectories) == 0 {
		return ""
	}
	return strings.Join(clearKeepsSubdirectories, "/, ") + "/"
}

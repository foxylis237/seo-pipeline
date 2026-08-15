package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// clearKeepsSubdirectories перечисляет то, что переживает очистку статьи. Список пуст: цель
// команды — состояние «статья не генерировалась», и уцелевшие логи ей противоречат. Строка
// вида status=completed в логе очищенной статьи — уже неправда, и прочитавший её получает
// неверную картину вместо истории.
//
// Список, а не константа, намеренно: вернуть логи — это одна строка здесь, без правки логики.
var clearKeepsSubdirectories = []string{}

// findArticleDirectoryForClear ищет каталог статьи по external_id и отличает «не найден» от
// ошибки. Отдельный поиск, а не resolveArticleDirectory: тому отсутствие каталога — ошибка,
// потому что он обслуживает этапы, которым есть что писать. Очистке отсутствие каталога —
// штатный случай, и менять общий резолвер ради неё нельзя: на нём висят все остальные этапы.
//
// Несколько каталогов на один external_id остаются ошибкой и здесь: гадать нельзя, стереть
// чужой каталог — ровно та потеря данных, от которой команда должна защищать.
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

// clearableEntries возвращает элементы верхнего уровня каталога статьи, которые подлежат
// удалению, — то есть всё, кроме сохраняемого.
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

// ClearArticleArtifacts удаляет с диска всё, что статья произвела после импорта, включая
// опустевший каталог статьи. Возвращает удалённые пути относительно корня вывода.
//
// Удаляется всё лишнее, а не перечисленный список артефактов: в каталоге статьи не лежит
// ничего, кроме произведённого пайплайном, а список пришлось бы дополнять при каждом новом
// этапе — и он молча отставал бы. ResetGeneratedArtifacts работает наоборот, по списку, но у
// него другая задача: сохранить research и отдать статью в regenerate.
//
// Отсутствие каталога — не ошибка: статью могли импортировать и ни разу не запускать, и это
// ровно то состояние, к которому команда ведёт. Очистка идемпотентна.
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

	// Когда не сохраняется ничего, пустой каталог статьи — такой же след прогона, как его
	// содержимое: у ни разу не запускавшейся статьи каталога нет вовсе. Удаляется только
	// опустевший: os.Remove на непустом откажет и не унесёт то, что уцелело после сбоя.
	if len(clearKeepsSubdirectories) == 0 && len(failures) == 0 {
		if removeErr := os.Remove(root); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("удалить каталог статьи %s: %w", directory, removeErr))
		}
	}

	// Удалённое возвращается вместе с ошибкой: вызывающий обязан знать, что часть файлов уже
	// исчезла, даже если очистка не доведена до конца.
	return removed, errors.Join(failures...)
}

// CountArticleArtifacts считает элементы верхнего уровня каталога статьи, которые удалит
// ClearArticleArtifacts, и возвращает сам каталог для отчёта. Пустое имя означает, что
// каталога у статьи ещё нет.
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

// ClearKeepsDescription перечисляет сохраняемые подкаталоги для отчёта команды. Пустая строка
// означает, что от статьи на диске не остаётся ничего.
func ClearKeepsDescription() string {
	if len(clearKeepsSubdirectories) == 0 {
		return ""
	}
	return strings.Join(clearKeepsSubdirectories, "/, ") + "/"
}

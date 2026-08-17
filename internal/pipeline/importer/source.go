package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkbookExtension — расширение книги импорта. Имя файла смысла не несёт, значимо только оно.
const WorkbookExtension = ".xlsx"

// ResolveWorkbook возвращает книгу Excel, которую нужно импортировать.
//
// Явно заданный путь выигрывает всегда: если человек назвал файл, гадать за него нельзя.
// Иначе книга ищется в каталоге задачи по расширению — имя файла значения не имеет, поэтому
// единственный xlsx там и есть ответ. Пустой каталог и несколько книг — это не выбор по
// умолчанию, а вопрос к человеку, и ошибка обязана назвать, что именно поправить.
//
// Функция одна на все точки входа (import, import-check, dry-run): правило выбора файла
// должно быть одним и тем же везде, иначе проверка импорта прочитает не то, что импорт.
func ResolveWorkbook(configuredPath, directory string) (string, error) {
	if path := strings.TrimSpace(configuredPath); path != "" {
		return path, nil
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return "", fmt.Errorf("не задан ни INPUT_FILE_PATH, ни каталог импорта задачи")
	}

	workbooks, err := findWorkbooks(directory)
	if err != nil {
		return "", err
	}
	switch len(workbooks) {
	case 1:
		return filepath.Join(directory, workbooks[0]), nil
	case 0:
		return "", fmt.Errorf(
			"в каталоге %s нет ни одного файла %s — положите туда Excel импорта или задайте INPUT_FILE_PATH",
			directory, WorkbookExtension,
		)
	default:
		return "", fmt.Errorf(
			"в каталоге %s несколько файлов %s (%s) — оставьте один или задайте INPUT_FILE_PATH",
			directory, WorkbookExtension, strings.Join(workbooks, ", "),
		)
	}
}

// findWorkbooks возвращает имена книг каталога в предсказуемом порядке.
//
// Временные файлы Excel (`~$книга.xlsx`, появляются, пока книга открыта) и скрытые файлы
// пропускаются: иначе открытый в Excel файл превращал бы единственную книгу в «несколько».
func findWorkbooks(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"каталог импорта %s не найден — создайте его и положите туда Excel импорта",
				directory,
			)
		}
		return nil, fmt.Errorf("прочитать каталог импорта %s: %w", directory, err)
	}

	workbooks := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.EqualFold(filepath.Ext(name), WorkbookExtension) {
			continue
		}
		workbooks = append(workbooks, name)
	}
	sort.Strings(workbooks)
	return workbooks, nil
}

package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, directory string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Явный путь не проверяется и не подменяется: человек назвал файл, гадать за него нельзя.
func TestResolveWorkbookPrefersConfiguredPath(t *testing.T) {
	directory := t.TempDir()
	writeFiles(t, directory, "из-каталога.xlsx")

	got, err := ResolveWorkbook("input/other/задано.xlsx", directory)
	if err != nil {
		t.Fatal(err)
	}
	if got != "input/other/задано.xlsx" {
		t.Fatalf("ResolveWorkbook() = %q, want the configured path", got)
	}
}

// Имя книги значения не имеет: единственный xlsx каталога и есть книга импорта.
func TestResolveWorkbookFindsSingleWorkbookByAnyName(t *testing.T) {
	directory := t.TempDir()
	writeFiles(t, directory, "Статьи на август.xlsx", "readme.txt", "черновик.csv")

	got, err := ResolveWorkbook("", directory)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(directory, "Статьи на август.xlsx"); got != want {
		t.Fatalf("ResolveWorkbook() = %q, want %q", got, want)
	}
}

// Открытая в Excel книга оставляет рядом `~$книга.xlsx`. Считать её второй книгой нельзя:
// импорт ломался бы ровно тогда, когда человек заглянул в файл.
func TestResolveWorkbookSkipsExcelTemporaryFiles(t *testing.T) {
	directory := t.TempDir()
	writeFiles(t, directory, "книга.xlsx", "~$книга.xlsx", ".скрытая.xlsx")

	got, err := ResolveWorkbook("", directory)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(directory, "книга.xlsx"); got != want {
		t.Fatalf("ResolveWorkbook() = %q, want %q", got, want)
	}
}

// Пустой каталог — вопрос к человеку, и ошибка обязана назвать каталог, куда класть файл.
func TestResolveWorkbookReportsEmptyDirectory(t *testing.T) {
	directory := t.TempDir()
	writeFiles(t, directory, "заметки.txt")

	_, err := ResolveWorkbook("", directory)
	if err == nil {
		t.Fatal("ResolveWorkbook() error = nil, want error")
	}
	if !strings.Contains(err.Error(), directory) {
		t.Fatalf("error = %v, want the directory named", err)
	}
}

// Несколько книг — тоже вопрос к человеку, и ошибка обязана перечислить, между чем выбирать.
func TestResolveWorkbookListsSeveralWorkbooks(t *testing.T) {
	directory := t.TempDir()
	writeFiles(t, directory, "вторая.xlsx", "первая.xlsx")

	_, err := ResolveWorkbook("", directory)
	if err == nil {
		t.Fatal("ResolveWorkbook() error = nil, want error")
	}
	for _, name := range []string{"первая.xlsx", "вторая.xlsx"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error = %v, want %q listed", err, name)
		}
	}
}

func TestResolveWorkbookReportsMissingDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "нет-такого")

	_, err := ResolveWorkbook("", directory)
	if err == nil || !strings.Contains(err.Error(), directory) {
		t.Fatalf("error = %v, want the missing directory named", err)
	}
}

func TestResolveWorkbookRequiresSource(t *testing.T) {
	if _, err := ResolveWorkbook("", ""); err == nil {
		t.Fatal("ResolveWorkbook() error = nil, want error")
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveStageAttachmentsTakesSingleDocument(t *testing.T) {
	directory := t.TempDir()
	// Имя файла не фиксировано, поэтому оно намеренно произвольное. Посторонние файлы
	// каталога выбор не ломают: значимо расширение.
	document := filepath.Join(directory, "Регламент вёрстки 2026.pdf")
	writeFile(t, document)
	writeFile(t, filepath.Join(directory, "обложка.png"))
	writeFile(t, filepath.Join(directory, ".DS_Store"))

	attachments, err := ResolveStageAttachments("html", directory)
	if err != nil {
		t.Fatalf("резолв документа стадии: %v", err)
	}
	if len(attachments) != 1 || attachments[0] != document {
		t.Fatalf("выбран не тот документ: %v", attachments)
	}
}

func TestResolveStageAttachmentsWithoutDirectoryIsNoAttachments(t *testing.T) {
	attachments, err := ResolveStageAttachments("structure", "  ")
	if err != nil {
		t.Fatalf("стадия без документов: %v", err)
	}
	if attachments != nil {
		t.Fatalf("у стадии без каталога появились документы: %v", attachments)
	}
}

func TestResolveStageAttachmentsRejectsAmbiguousDirectory(t *testing.T) {
	empty := t.TempDir()
	crowded := t.TempDir()
	writeFile(t, filepath.Join(crowded, "первый.pdf"))
	writeFile(t, filepath.Join(crowded, "второй.pdf"))

	tests := []struct {
		name      string
		directory string
		expect    string
	}{
		{name: "нет документов", directory: empty, expect: "нет ни одного документа"},
		{name: "несколько документов", directory: crowded, expect: "несколько документов"},
		{name: "каталога нет", directory: filepath.Join(empty, "нет-такого"), expect: "не найден"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveStageAttachments("html", test.directory)
			if err == nil {
				t.Fatal("каталог принят, хотя документ выбрать нельзя")
			}
			if !strings.Contains(err.Error(), test.expect) {
				t.Fatalf("ошибка не подсказывает, что поправить: %v", err)
			}
			if !strings.Contains(err.Error(), "html") {
				t.Fatalf("ошибка не называет стадию: %v", err)
			}
		})
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("подготовить файл %s: %v", path, err)
	}
}

// Регламент лежит у человека, а не в репозитории, и формат его меняется: тот же документ
// сегодня PDF, завтра простой текст. Стадия обязана принять и такой.
func TestResolveStageAttachmentsTakesPlainText(t *testing.T) {
	directory := t.TempDir()
	document := filepath.Join(directory, "reglament-statey-dpoprof.txt")
	writeFile(t, document)
	writeFile(t, filepath.Join(directory, ".gitkeep"))

	attachments, err := ResolveStageAttachments("html", directory)
	if err != nil {
		t.Fatalf("текстовый регламент не принят: %v", err)
	}
	if len(attachments) != 1 || attachments[0] != document {
		t.Fatalf("выбран не тот документ: %v", attachments)
	}
}

package deepseekweb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachmentPathsResolvesDocuments(t *testing.T) {
	directory := t.TempDir()
	document := filepath.Join(directory, "Регламент вёрстки.pdf")
	if err := os.WriteFile(document, []byte("%PDF"), 0o600); err != nil {
		t.Fatalf("подготовить документ: %v", err)
	}

	paths, markers, err := attachmentPaths([]string{document})
	if err != nil {
		t.Fatalf("разрешить документ: %v", err)
	}
	if len(paths) != 1 || !filepath.IsAbs(paths[0]) {
		t.Fatalf("путь документа не абсолютный: %v", paths)
	}
	// Страница показывает имя обрезанным, поэтому ищется его начало и в нижнем регистре.
	if len(markers) != 1 || !strings.HasPrefix("регламент вёрстки", markers[0]) {
		t.Fatalf("признак документа на странице неверен: %v", markers)
	}
}

func TestAttachmentPathsReportsMissingDocument(t *testing.T) {
	_, _, err := attachmentPaths([]string{filepath.Join(t.TempDir(), "нет-такого.pdf")})
	if err == nil {
		t.Fatal("отсутствующий документ принят")
	}
	if !strings.Contains(err.Error(), "нет-такого.pdf") {
		t.Fatalf("ошибка не называет документ: %v", err)
	}
}

func TestAttachmentMarkerKeepsShortNamesWhole(t *testing.T) {
	if got := attachmentMarker("html.pdf"); got != "html" {
		t.Fatalf("короткое имя обрезано: %q", got)
	}
	if got := attachmentMarker("Регламент HTML вёрстки.pdf"); len([]rune(got)) != attachmentMarkerRunes {
		t.Fatalf("длинное имя не обрезано: %q", got)
	}
}

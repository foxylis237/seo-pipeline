package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
)

func writeImage(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, articleImagesSubdir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("каталог обложек: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("обложка %s: %v", name, err)
	}
	return path
}

func TestLocateFindsCoverByExternalIDAndNamesItBySlug(t *testing.T) {
	dir := t.TempDir()
	path := writeImage(t, dir, "16.webp", []byte("webp"))

	image, err := newArticleImages(dir).Locate("16", "razryady-gazosvarshchikov")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if image.Path != path {
		t.Fatalf("путь = %q, ожидался %q", image.Path, path)
	}
	// На диске файл назван external_id — так его кладёт человек. В библиотеку он уходит
	// slug'ом: адрес картинки виден в выдаче, и «16.webp» там — потерянные слова.
	if image.MediaName != "razryady-gazosvarshchikov.webp" {
		t.Fatalf("имя в библиотеке = %q", image.MediaName)
	}
	if image.MIMEType != "image/webp" {
		t.Fatalf("тип = %q", image.MIMEType)
	}
	if image.Size != 4 {
		t.Fatalf("размер = %d", image.Size)
	}
}

// Порядок расширений фиксирован: при двух файлах одной статьи выбор обязан быть
// предсказуемым, а не зависеть от порядка чтения каталога.
func TestLocatePrefersWebpOverOtherFormats(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, dir, "16.png", []byte("png"))
	writeImage(t, dir, "16.webp", []byte("webp"))

	image, err := newArticleImages(dir).Locate("16", "slug")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if !strings.HasSuffix(image.Path, "16.webp") {
		t.Fatalf("выбран %q", image.Path)
	}
}

func TestLocateAcceptsOtherSupportedFormats(t *testing.T) {
	for name, wantType := range map[string]string{
		"16.jpg":  "image/jpeg",
		"16.jpeg": "image/jpeg",
		"16.png":  "image/png",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeImage(t, dir, name, []byte("картинка"))

			image, err := newArticleImages(dir).Locate("16", "slug")
			if err != nil {
				t.Fatalf("Locate: %v", err)
			}
			if image.MIMEType != wantType {
				t.Fatalf("тип = %q, ожидался %q", image.MIMEType, wantType)
			}
		})
	}
}

func TestLocateFallsBackToExternalIDWhenSlugIsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, dir, "16.webp", []byte("webp"))

	image, err := newArticleImages(dir).Locate("16", "   ")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if image.MediaName != "16.webp" {
		t.Fatalf("имя в библиотеке = %q", image.MediaName)
	}
}

func TestLocateRefusesUnusableCover(t *testing.T) {
	t.Run("обложки нет", func(t *testing.T) {
		dir := t.TempDir()
		_, err := newArticleImages(dir).Locate("16", "slug")
		if err == nil {
			t.Fatal("отсутствие обложки принято за успех")
		}
		// Человеку нужно знать, куда класть файл и под каким именем.
		if !strings.Contains(err.Error(), articleImagesSubdir) || !strings.Contains(err.Error(), "16.webp") {
			t.Fatalf("ошибка не говорит, что делать: %v", err)
		}
	})
	t.Run("файл пуст", func(t *testing.T) {
		dir := t.TempDir()
		writeImage(t, dir, "16.webp", nil)
		if _, err := newArticleImages(dir).Locate("16", "slug"); err == nil {
			t.Fatal("пустой файл принят")
		}
	})
	t.Run("файл тяжелее потолка", func(t *testing.T) {
		dir := t.TempDir()
		path := writeImage(t, dir, "16.webp", []byte("webp"))
		// Разреженный файл: важен размер, а не мегабайты на диске под тестом.
		if err := os.Truncate(path, wordpress.MaxMediaBytes+1); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		_, err := newArticleImages(dir).Locate("16", "slug")
		if err == nil {
			t.Fatal("файл сверх потолка принят: отказ обязан приходить с диска, а не из сети")
		}
		if !strings.Contains(err.Error(), "сожмите") {
			t.Fatalf("ошибка не подсказывает решение: %v", err)
		}
	})
	t.Run("на месте файла каталог", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, articleImagesSubdir, "16.webp"), 0o755); err != nil {
			t.Fatalf("каталог: %v", err)
		}
		if _, err := newArticleImages(dir).Locate("16", "slug"); err == nil {
			t.Fatal("каталог принят за обложку")
		}
	})
}

func TestLoadReadsFileAsIs(t *testing.T) {
	dir := t.TempDir()
	content := []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0xFF}
	writeImage(t, dir, "16.webp", content)
	images := newArticleImages(dir)

	image, err := images.Locate("16", "razryady")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	file, err := images.Load(image)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(file.Bits) != string(content) {
		t.Fatalf("содержимое изменено: %v", file.Bits)
	}
	if file.Name != "razryady.webp" || file.MIMEType != "image/webp" {
		t.Fatalf("файл = %+v", file)
	}
}

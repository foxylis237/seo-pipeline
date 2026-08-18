package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
)

// articleImagesSubdir — каталог обложек внутри входного каталога задачи.
//
// Обложки лежат рядом с книгой импорта (input/<задача>/images), а не в OUTPUT_DIR: это
// входные данные, которые кладёт человек, а не артефакт, собранный пайплайном. Разница
// практическая — OUTPUT_DIR чистят reset и clear, входные данные они не трогают.
const articleImagesSubdir = "images"

// articleImageTypes — форматы, которые команда готова отправить в блог.
//
// Порядок значим: он же порядок поиска файла. Тип определяется расширением по этому списку,
// а не через mime.TypeByExtension — тот заглядывает в /etc/mime.types и на разных машинах
// отвечает по-разному, а тип уходит в WordPress и сверяется с его списком разрешённых.
var articleImageTypes = []struct {
	Extension string
	MIMEType  string
}{
	{".webp", "image/webp"},
	{".jpg", "image/jpeg"},
	{".jpeg", "image/jpeg"},
	{".png", "image/png"},
}

// wordPressImage — найденная на диске обложка, ещё не прочитанная.
//
// Отдельный шаг «нашли, но не читаем» нужен сухому прогону: показать человеку, какой файл
// уйдёт и сколько он весит, можно и не поднимая в память двадцать мегабайт.
type wordPressImage struct {
	// Path — путь на диске, для сообщений и чтения.
	Path string
	// MediaName — имя файла в медиабиблиотеке WordPress.
	MediaName string
	MIMEType  string
	Size      int64
}

// articleImages ищет обложки статей в каталоге задачи.
type articleImages struct {
	dir string
}

// newArticleImages собирает источник обложек по входному каталогу задачи.
func newArticleImages(inputDir string) articleImages {
	return articleImages{dir: filepath.Join(inputDir, articleImagesSubdir)}
}

// Locate находит обложку статьи, не читая её содержимого.
//
// Имя файла на диске — external_id статьи: так его кладёт человек, и другого источника
// соответствия «статья ⇄ картинка» у нас нет. В медиабиблиотеку тот же файл уходит под
// slug'ом статьи: адрес картинки виден в выдаче, и «19.webp» там — потерянные ключевые слова.
func (a articleImages) Locate(externalID, slug string) (wordPressImage, error) {
	for _, imageType := range articleImageTypes {
		path := filepath.Join(a.dir, externalID+imageType.Extension)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			return wordPressImage{}, fmt.Errorf("обложка %s — не обычный файл", path)
		}
		if info.Size() == 0 {
			return wordPressImage{}, fmt.Errorf("обложка %s пуста", path)
		}
		if info.Size() > wordpress.MaxMediaBytes {
			return wordPressImage{}, fmt.Errorf(
				"обложка %s весит %.1f МБ при потолке %d МБ — сожмите картинку",
				path, float64(info.Size())/(1<<20), wordpress.MaxMediaBytes>>20)
		}
		return wordPressImage{
			Path:      path,
			MediaName: mediaFileName(externalID, slug, imageType.Extension),
			MIMEType:  imageType.MIMEType,
			Size:      info.Size(),
		}, nil
	}
	return wordPressImage{}, fmt.Errorf(
		"нет обложки статьи %s: положите файл %s%s в %s (допустимы %s)",
		externalID, externalID, articleImageTypes[0].Extension, a.dir, supportedImageExtensions())
}

// Load читает найденную обложку.
func (a articleImages) Load(image wordPressImage) (wordpress.MediaFile, error) {
	bits, err := os.ReadFile(image.Path)
	if err != nil {
		return wordpress.MediaFile{}, fmt.Errorf("прочитать обложку %s: %w", image.Path, err)
	}
	return wordpress.MediaFile{Name: image.MediaName, MIMEType: image.MIMEType, Bits: bits}, nil
}

// mediaFileName собирает имя файла в медиабиблиотеке.
//
// Slug статьи — тот же, которым названы каталоги артефактов, он приходит из колонки
// image_slug книги импорта. Пустым он у прошедшей пайплайн статьи не бывает, но запасной
// вариант оставлен: имя из external_id некрасиво, а публикация без обложки хуже.
func mediaFileName(externalID, slug, extension string) string {
	name := strings.TrimSpace(slug)
	if name == "" {
		name = externalID
	}
	return name + extension
}

func supportedImageExtensions() string {
	extensions := make([]string, 0, len(articleImageTypes))
	for _, imageType := range articleImageTypes {
		extensions = append(extensions, imageType.Extension)
	}
	return strings.Join(extensions, ", ")
}

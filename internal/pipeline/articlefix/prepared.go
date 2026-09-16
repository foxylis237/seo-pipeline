package articlefix

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Готовая правка — текст статьи и поля записи, исправленные заранее, вне прогона.
//
// Режим нужен задаче, у которой правку пишет не модель внутри run: пачку в восемь десятков
// страниц готовят параллельно, а браузерный профиль DeepSeek один на все процессы и защищён
// flock. Остальной поток не меняется — оригинал ложится на диск раньше записи, запись идёт
// через Blog.Write, после неё сверка чтением и отметка updated_post_at.
//
// Раскладка одной статьи в каталоге готовых правок:
//
//	<индекс>-<слаг>/base.html            тело записи, по которому правка готовилась
//	<индекс>-<слаг>/article.html         исправленное тело
//	<индекс>-<слаг>/base_fields.json     поля записи на момент подготовки
//	<индекс>-<слаг>/fields/<ключ>.html   новое значение поля
//
// Снимки base.* — не украшение, а защита: между подготовкой и записью страницу могли
// поправить в админке, и готовая правка молча стёрла бы чужую работу. Живая запись поэтому
// сверяется со снимком, и расхождение роняет статью до записи.
const (
	PreparedBaseFile       = "base.html"
	PreparedArticleFile    = "article.html"
	PreparedBaseFieldsFile = "base_fields.json"
	PreparedFieldsFolder   = "fields"
	// OriginalFieldsFile — прежние значения полей, которые правка меняет. Ложится в original/
	// рядом с телом и заголовком по той же причине: после записи их больше нигде не взять.
	OriginalFieldsFile = "fields.json"
)

// PreparedFAQFields — поля, которые готовой правке разрешено менять: вопросы и ответы блока
// частых вопросов.
//
// Список закрытый намеренно: файлы в каталог правок кладёт не код, и ключ с опечаткой или
// чужое поле записи не имеют права уйти в живой блог.
var PreparedFAQFields = regexp.MustCompile(`^faq_loop_\d+_faq_(question|answer)$`)

// ErrPreparedStale — живая запись разошлась со снимком, по которому готовилась правка.
var ErrPreparedStale = errors.New("страница в блоге изменилась после подготовки правки")

// Prepared берёт правку статьи из каталога готовых правок, а не у модели.
//
// allowed называет поля записи, которые правке разрешено менять; файл поля вне списка роняет
// статью до записи.
func Prepared(dir string, allowed *regexp.Regexp) Option {
	return func(f *Flow) {
		f.preparedDir = dir
		f.preparedFields = allowed
	}
}

// preparedRewrite — готовая правка одной статьи, уже сверенная с живой записью.
type preparedRewrite struct {
	ArticleHTML string
	// TextChanged — отличается ли исправленное тело от снимка. Правка одних только полей
	// законна.
	TextChanged bool
	Fields      []Field
	// Original — живые значения меняемых полей: они ложатся в original/ до записи.
	Original map[string]string
}

// prepare читает готовую правку, если поток на неё настроен, и кладёт прежние значения
// меняемых полей в original/ — раньше, чем что-либо уйдёт в блог.
func (f *Flow) prepare(article Article, current Post) (preparedRewrite, error) {
	if f.preparedDir == "" {
		return preparedRewrite{}, nil
	}
	prepared, err := f.loadPrepared(article, current)
	if err != nil {
		return preparedRewrite{}, err
	}
	if len(prepared.Original) > 0 {
		content, marshalErr := json.MarshalIndent(prepared.Original, "", "  ")
		if marshalErr != nil {
			return preparedRewrite{}, fmt.Errorf("сохранить прежние поля записи: %w", marshalErr)
		}
		if _, saveErr := f.artifacts.Save(article.ExternalID, article.Slug,
			OriginalFolder, OriginalFieldsFile, string(content)); saveErr != nil {
			return preparedRewrite{}, saveErr
		}
	}
	return prepared, nil
}

// loadPrepared читает готовую правку статьи и сверяет её снимки с живой записью.
func (f *Flow) loadPrepared(article Article, current Post) (preparedRewrite, error) {
	dir := filepath.Join(f.preparedDir, DirectoryName(article.ExternalID, article.Slug))
	base, err := readPreparedFile(dir, PreparedBaseFile)
	if err != nil {
		return preparedRewrite{}, err
	}
	if !sameMarkup(base, current.ContentHTML) {
		return preparedRewrite{}, fmt.Errorf("%w: тело записи %d не совпадает с %s",
			ErrPreparedStale, current.ID, filepath.Join(dir, PreparedBaseFile))
	}
	rewritten, err := readPreparedFile(dir, PreparedArticleFile)
	if err != nil {
		return preparedRewrite{}, err
	}
	prepared := preparedRewrite{
		ArticleHTML: rewritten,
		TextChanged: !sameMarkup(base, rewritten),
		Original:    map[string]string{},
	}
	if err := f.loadPreparedFields(dir, current, &prepared); err != nil {
		return preparedRewrite{}, err
	}
	// Правка, которая ничего не меняет, — почти наверняка не сделанная правка: страницу
	// пропустили при подготовке. Отметить её переписанной значило бы потерять её молча.
	if !prepared.TextChanged && len(prepared.Fields) == 0 {
		return preparedRewrite{}, fmt.Errorf("готовая правка в %s ничего не меняет: "+
			"ни текст, ни поля не отличаются от снимка", dir)
	}
	return prepared, nil
}

// loadPreparedFields собирает поля записи, которые готовая правка меняет.
//
// Файл, совпавший со снимком, не отправляется: заготовка кладёт копию каждого поля, а
// меняется из них обычно меньшинство. Поле уходит с идентификатором postmeta из живой
// записи — без него wp.editPost завёл бы второе поле с тем же ключом.
func (f *Flow) loadPreparedFields(dir string, current Post, prepared *preparedRewrite) error {
	fieldsDir := filepath.Join(dir, PreparedFieldsFolder)
	entries, err := os.ReadDir(fieldsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("прочитать поля готовой правки в %s: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	baseFields, err := readPreparedBaseFields(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		key := strings.TrimSuffix(name, filepath.Ext(name))
		if f.preparedFields == nil || !f.preparedFields.MatchString(key) {
			return fmt.Errorf("готовая правка в %s меняет поле %q, а менять его задаче не разрешено", dir, key)
		}
		value, readErr := readPreparedFile(fieldsDir, name)
		if readErr != nil {
			return readErr
		}
		base, ok := baseFields[key]
		if !ok {
			return fmt.Errorf("поля %q нет в снимке %s", key, filepath.Join(dir, PreparedBaseFieldsFile))
		}
		if !sameMarkup(base, current.Fields[key]) {
			return fmt.Errorf("%w: поле %s записи %d не совпадает со снимком", ErrPreparedStale, key, current.ID)
		}
		if sameMarkup(value, base) {
			continue
		}
		id := strings.TrimSpace(current.FieldIDs[key])
		if id == "" {
			return fmt.Errorf("у записи %d нет идентификатора поля %q: правка завела бы второе поле с тем же ключом",
				current.ID, key)
		}
		prepared.Fields = append(prepared.Fields, Field{ID: id, Key: key, Value: value})
		prepared.Original[key] = current.Fields[key]
	}
	return nil
}

// planPrepared показывает в плане, готова ли правка статьи и что она изменит.
//
// Ничего не пишет. Снимок сверяется с живой записью, покрытие — тем же признаком, что в
// прогоне: план затем и нужен, чтобы отказы run стали видны все сразу и до записи.
func (f *Flow) planPrepared(article Article, current Post, planned *PlannedChange) {
	prepared, err := f.loadPrepared(article, current)
	if err == nil {
		err = f.covers(current.ContentHTML, prepared.ArticleHTML)
	}
	if err != nil {
		planned.PreparedProblem = err.Error()
		return
	}
	planned.PreparedReady = true
	planned.PreparedTextChanged = prepared.TextChanged
	planned.PreparedFields = len(prepared.Fields)
}

// readPreparedFile читает файл готовой правки. Пустой файл — отказ: пустое тело или пустой
// ответ FAQ в живом блоге хуже отсутствия правки.
func readPreparedFile(dir, name string) (string, error) {
	path := filepath.Join(dir, name)
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("прочитать готовую правку %q: %w", path, err)
	}
	if strings.TrimSpace(string(content)) == "" {
		return "", fmt.Errorf("готовая правка %q пуста", path)
	}
	return string(content), nil
}

func readPreparedBaseFields(dir string) (map[string]string, error) {
	path := filepath.Join(dir, PreparedBaseFieldsFile)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("прочитать снимок полей %q: %w", path, err)
	}
	var fields map[string]string
	if err := json.Unmarshal(content, &fields); err != nil {
		return nil, fmt.Errorf("разобрать снимок полей %q: %w", path, err)
	}
	return fields, nil
}

// preparedSpaceRE — пробельные символы при сверке снимка с живой записью.
var preparedSpaceRE = regexp.MustCompile(`\s+`)

// sameMarkup сравнивает разметку целиком, с тегами, но без разницы в пробелах: снимок и
// запись — одна и та же строка из базы сайта, и расходиться у них может разве что перевод
// строки. Текстовой сверки здесь мало: правка в админке могла поменять одну ссылку.
func sameMarkup(left, right string) bool {
	return preparedSpaceRE.ReplaceAllString(strings.TrimSpace(left), " ") ==
		preparedSpaceRE.ReplaceAllString(strings.TrimSpace(right), " ")
}

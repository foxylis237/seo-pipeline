package obuch2

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
	"github.com/foxylis237/seo-pipeline/internal/tasks/obuch1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof2"
)

// projectRoot — корень дерева относительно каталога пакета.
const projectRoot = "../../.."

// Ни один путь, префикс или схема задачи не должны совпадать ни с pprof_2, ни с obuch_1.
//
// С pprof_2 их роднит замысел: обе пишут коммерческие страницы услуг тремя чатами. С
// obuch_1 — площадка: у них один сайт и одни учётные данные. Общий каталог, общая схема или
// общий префикс переменных с любой из двух означали бы, что прогон одной задачи пишет в
// данные другой, — и тем важнее это проверить, что задачи похожи.
func TestProfileSharesNothingWithNeighbours(t *testing.T) {
	own := Profile()
	for _, other := range []tasks.Profile{pprof2.Profile(), obuch1.Profile()} {
		for _, field := range []struct {
			name       string
			own, other string
		}{
			{name: "Name", own: own.Name, other: other.Name},
			{name: "Command", own: own.Command, other: other.Command},
			{name: "InputDir", own: own.InputDir, other: other.InputDir},
			{name: "OutputDir", own: own.OutputDir, other: other.OutputDir},
			{name: "PromptsDir", own: own.PromptsDir, other: other.PromptsDir},
			{name: "TemplatePath", own: own.TemplatePath, other: other.TemplatePath},
			{name: "LLMConfigPath", own: own.LLMConfigPath, other: other.LLMConfigPath},
			{name: "ImportReportsDir", own: own.ImportReportsDir, other: other.ImportReportsDir},
			{name: "DiagnosticsDir", own: own.DiagnosticsDir, other: other.DiagnosticsDir},
			{name: "DBSchema", own: own.DBSchema, other: other.DBSchema},
			{name: "EnvPrefix", own: own.EnvPrefix, other: other.EnvPrefix},
		} {
			if field.own == "" {
				t.Fatalf("поле профиля %s пусто", field.name)
			}
			if field.own == field.other {
				t.Fatalf("поле профиля %s совпадает с %s: %q", field.name, other.Name, field.own)
			}
		}
	}
}

// Каталоги и файлы, названные профилем, обязаны существовать в дереве проекта: иначе первая
// же команда падает на чтении шаблона или схемы стадий.
func TestProfilePathsExist(t *testing.T) {
	profile := Profile()
	for _, path := range []string{
		profile.InputDir,
		profile.PromptsDir,
		profile.TemplatePath,
		profile.LLMConfigPath,
		CTACardPath,
	} {
		if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(path))); err != nil {
			t.Fatalf("путь профиля %s недоступен: %v", path, err)
		}
	}
}

// Каждая стадия схемы обязана иметь свой файл промпта, и все файлы — лежать в каталоге
// задачи: промпты соседа брать нельзя, иначе правка одной площадки молча меняет другую.
func TestEveryStageHasItsOwnPrompt(t *testing.T) {
	text := readConfig(t)
	for _, stage := range Stages {
		if !strings.Contains(text, "\n    "+stage+":\n") {
			t.Fatalf("стадия %q не описана в %s", stage, Profile().LLMConfigPath)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		prompt, found := strings.CutPrefix(strings.TrimSpace(line), "prompt: ")
		if !found {
			continue
		}
		if !strings.HasPrefix(prompt, Profile().PromptsDir+"/") {
			t.Fatalf("промпт %q лежит вне каталога задачи", prompt)
		}
		if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(prompt))); err != nil {
			t.Fatalf("промпт %s недоступен: %v", prompt, err)
		}
	}
}

// Документов у задачи нет ни у одной стадии, и это решение.
//
// Вёрстка площадки целиком живёт в промпте html.txt, а регламента страницы услуги у неё нет
// вовсе: форму задаёт скелет внутри structure.txt. Пустой каталог вложений роняет стадию до
// обращения к модели, поэтому лишний attachments_dir стоил бы оплаченного прогона.
func TestNoStageAsksForAttachments(t *testing.T) {
	if attached := stageAttachments(t); len(attached) > 0 {
		t.Fatalf("стадии просят документы, которых у задачи нет: %v", attached)
	}
}

// Пока у задачи нет своей папки Drive, выгрузка промптов обязана быть выключена: документ
// ищется по имени «Промт: <заголовок>», а названия страниц услуг совпадают с pprof_2 —
// прогон молча перезаписал бы чужой промпт.
func TestGoogleDocsStayOffWhileFolderIsUnset(t *testing.T) {
	if Profile().GoogleFolderURL != "" {
		return
	}
	if !strings.Contains(readConfig(t), "\n  google_docs: false\n") {
		t.Fatal("папка Drive не задана, а выгрузка промптов включена — она перезапишет документы pprof_2")
	}
}

// Публикация после прогона выключена, пока раскладка полей не сверена с живой записью:
// отменить публикацию приложение не умеет.
func TestPublishAfterRunStaysOffUntilVerified(t *testing.T) {
	if !strings.Contains(readConfig(t), "\n  publish_after_run: false\n") {
		t.Fatal("публикация после прогона включена, а раскладка полей площадки ещё не сверена с живой записью")
	}
}

// Поиск в интернете включён ровно у одной стадии — article, и это её свойство, а не задачи.
//
// Искать нужно одно: источник под таблицей заработка, который обязан быть сайтом, который
// модель правда открыла. Работает признак там потому, что переключатель относится к чату
// целиком и трогается только на первом его сообщении, а чат 2 открывает именно article. У
// review тот же признак не сработал бы вовсе и молча — поэтому стадия здесь названа поимённо.
func TestSearchIsOnlyOnTheStageThatOpensItsChat(t *testing.T) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	profile := Profile()
	cfg, err := config.LoadLLMConfigForStages(profile.LLMConfigPath, profile.LLMStages, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range profile.LLMStages {
		got := cfg.Stages[name].Search
		if name == StageArticle && !got {
			t.Fatal("у стадии article выключен поиск: источник под таблицей заработка станет выдуманным")
		}
		if name != StageArticle && got {
			t.Fatalf("поиск включён у стадии %s: она не открывает свой чат, и переключатель промолчит", name)
		}
	}
}

// Бюджет стадии обязан вмещать не одну попытку: отказ провайдера лечится повтором, а повтор
// не начнётся, пока первая попытка забирает бюджет целиком.
func TestStageBudgetsLeaveRoomForRetries(t *testing.T) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	profile := Profile()
	cfg, err := config.LoadLLMConfigForStages(profile.LLMConfigPath, profile.LLMStages, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range profile.LLMStages {
		stage := cfg.Stages[name]
		if stage.AttemptTimeout >= stage.Timeout {
			t.Fatalf("стадия %s: attempt_timeout %v не короче бюджета %v", name, stage.AttemptTimeout, stage.Timeout)
		}
		if spare := stage.Timeout - stage.AttemptTimeout; spare < 4*time.Minute {
			t.Fatalf("стадия %s: на паузы между повторами остаётся %v", name, spare)
		}
	}
}

// Признаки площадки — то, ради чего задача и заведена отдельно. Снятый признак не ломает
// сборку и не роняет тест соседа: он молча отправит в чужой блог чужие поля, а увидеть это
// можно будет только на живой записи.
func TestProfileDeclaresForeignSiteAndServicePages(t *testing.T) {
	profile := Profile()
	if !profile.PlainServicePages {
		t.Fatal("снят PlainServicePages: задача получит блоговую раскладку и отправит поля ACF, которых у площадки нет")
	}
	if profile.CommercialPages || profile.PlainBlogPages {
		t.Fatal("выставлен признак чужой раскладки: страница услуги площадки без ACF — это третий случай, а не пара из прежних")
	}
	if profile.CatalogSite != catalog.SiteObuchim {
		t.Fatalf("площадка каталога %q: catalog-sync стёр бы каталог чужого сайта, "+
			"а раскладка искала бы рубрику в чужой таксономии", profile.CatalogSite)
	}
	if profile.RelatedCourses {
		t.Fatal("включены RelatedCourses: блока связанных курсов под страницей услуги нет")
	}
	// Стадии info у задачи нет, и метаданных она не пишет вовсе.
	if !profile.WithoutMetadataStage {
		t.Fatal("снят WithoutMetadataStage: раннер полного прогона будет ждать метаданных вечно")
	}
	// MetadataFAQOnly остаётся нулевым: признак потребовал бы на публикации непустой FAQ, а
	// заполнять его нечем — поля под частые вопросы у площадки нет, и они живут в теле
	// страницы. Цена решения — колонка tldr в схеме, в которую никто не пишет.
	if profile.MetadataFAQOnly {
		t.Fatal("выставлен MetadataFAQOnly: публикация потребует FAQ, которого задача не собирает")
	}
}

// Необщие колонки обязаны быть известны репозиторию: иначе опечатка в профиле обнаружилась бы
// пустым полем в result.md, а не отказом на старте.
func TestInputColumnsAreKnownToRepository(t *testing.T) {
	if err := repository.ValidateExtraInputColumns(InputColumns); err != nil {
		t.Fatalf("колонки профиля не приняты репозиторием: %v", err)
	}
}

// Задачи не заимствуют колонки друг у друга. Исключения перечислены в
// repository.SharedInputColumns и только там: это поля, которые у обеих задач значат одно и
// то же. Случайное пересечение и осознанное различаются только намерением автора, поэтому
// список явный.
func TestInputColumnsDoNotOverlapWithNeighbours(t *testing.T) {
	for _, other := range []tasks.Profile{pprof2.Profile(), obuch1.Profile()} {
		if len(other.ExtraInputColumns) == 0 {
			t.Fatalf("задача %s перестала объявлять свои колонки", other.Name)
		}
		for _, own := range InputColumns {
			if slices.Contains(repository.SharedInputColumns, own) {
				continue
			}
			if slices.Contains(other.ExtraInputColumns, own) {
				t.Fatalf("колонка %q объявлена и у obuch_2, и у %s", own, other.Name)
			}
		}
		for _, foreign := range other.ExtraInputColumns {
			if slices.Contains(repository.SharedInputColumns, foreign) {
				continue
			}
			if slices.Contains(InputColumns, foreign) {
				t.Fatalf("колонка %q задачи %s объявлена у obuch_2", foreign, other.Name)
			}
		}
	}
}

// Своя baseline описывает схему задачи целиком: и то, что у неё есть, и то, чего у неё нет.
//
// Лежать она обязана в своём каталоге: колонка, попавшая в чужой файл, оказалась бы в схеме
// соседа, и тот перестал бы стартовать на «unexpected column».
func TestOwnMigrationDescribesWholeSchema(t *testing.T) {
	schema := readMigrations(t, Name)
	for _, column := range InputColumns {
		if !strings.Contains(schema, column+" TEXT") {
			t.Fatalf("миграция задачи не заводит колонку %q", column)
		}
	}
	// Колонка tldr здесь есть, хотя в неё никто не пишет: её наличие выводится из
	// MetadataFAQOnly, а тот признак поднял бы на публикации требование FAQ. Уберут колонку
	// из файла — ValidateSchema остановит первую же команду на «missing column».
	if !strings.Contains(schema, "tldr TEXT") {
		t.Fatal("схема задачи не заводит tldr, хотя профиль не объявляет MetadataFAQOnly")
	}
	// Колонок, которых у задачи нет по замыслу, в её схеме быть не должно.
	for _, absent := range []string{
		"author TEXT", "links TEXT", "professions TEXT", "tags TEXT",
		"teachers TEXT", "section TEXT", "service_name TEXT",
	} {
		if strings.Contains(schema, absent) {
			t.Fatalf("схема задачи заводит колонку, которой у неё нет: %q", absent)
		}
	}

	foreign, err := filepath.Glob(filepath.Join(projectRoot, "migrations", "*", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range foreign {
		if filepath.Base(filepath.Dir(name)) == Name {
			continue
		}
		text, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		// Сравнивается именно таблица article_inputs, а не файл целиком: у задач правки и
		// аудита её нет вовсе, а в их собственной таблице живёт своя колонка post_type —
		// тип записи, каким его отдала площадка. Это другое значение в другой таблице, и
		// объявлять его заимствованием было бы неправдой.
		columns := articleInputsBlock(string(text))
		if columns == "" {
			continue
		}
		for _, column := range InputColumns {
			// Общую колонку чужая миграция заводить обязана — она нужна и той задаче.
			if slices.Contains(repository.SharedInputColumns, column) {
				continue
			}
			if strings.Contains(columns, " "+column+" TEXT") {
				t.Fatalf("миграция %s/%s заводит колонку %q задачи obuch_2",
					filepath.Base(filepath.Dir(name)), filepath.Base(name), column)
			}
		}
	}
}

// articleInputsBlock вырезает объявление таблицы article_inputs из текста миграции.
//
// Пустая строка означает, что таблицы в файле нет: так устроены схемы задач правки и аудита,
// и колонке article_inputs там столкнуться не с чем.
func articleInputsBlock(migration string) string {
	const opening = "CREATE TABLE IF NOT EXISTS article_inputs ("
	start := strings.Index(migration, opening)
	if start < 0 {
		return ""
	}
	rest := migration[start+len(opening):]
	end := strings.Index(rest, "\n);")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// Шаблон карточки призыва обязан подставлять ровно те слоты, что объявлены структурой:
// шаблон с чужим плейсхолдером не отказывает, а молча подставляет пустую строку — и в блог
// уходит карточка с дырой на месте заголовка или надписи на кнопке.
func TestCTACardTemplateUsesDeclaredSlots(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(CTACardPath)))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	// Слоты — ровно те, что объявлены структурой и спрашиваются доспросом. Вёрстка снята с
	// живой страницы услуги целиком: оранжевая плашка с заголовком, двумя абзацами, списком
	// документов и кнопкой.
	for _, slot := range []string{"{{.Heading}}", "{{.Intro}}", "{{.ButtonText}}", "{{range .Documents}}"} {
		if !strings.Contains(text, slot) {
			t.Fatalf("шаблон плашки призыва не подставляет %s", slot)
		}
	}
	if strings.Contains(text, "{{.Badge}}") || strings.Contains(text, "{{.Qualification}}") {
		t.Fatal("в шаблоне остались слоты прежней блоговой карточки")
	}
	if !strings.Contains(text, "cta-course-zone") {
		t.Fatal("нет класса cta-course-zone — им площадка размечает плашку призыва")
	}
	// Классы, по которым код узнаёт уже нарисованный моделью призыв и не ставит второй.
	for _, marker := range []string{ctaCardMarker, ctaButtonMarker} {
		if !strings.Contains(text, marker) {
			t.Fatalf("в шаблоне карточки нет класса %s — по нему код узнаёт призыв", marker)
		}
	}
	// Вёрстка кнопки снята с живых страниц услуг площадки (бетонщики-арматурщики,
	// гид-переводчик) целиком, вместе с data-popup: ровно так там открывается форма заявки.
	// Прежний вывод «data-popup здесь мёртв» был снят с блога, а не со страницы услуги.
	if !strings.Contains(text, `class="service btn-primary feedback__event"`) {
		t.Fatal("классы кнопки разошлись с живой страницей услуги")
	}
	if !strings.Contains(text, `data-popup="popup-contact"`) {
		t.Fatal("в кнопке нет data-popup: на живых страницах услуг он есть")
	}
	// Кнопка открывает форму, а не ведёт на программу: ссылки и адреса у неё нет вовсе.
	// {{.Text}} здесь законен: это пояснение строки документа внутри range по Documents.
	for _, unknown := range []string{"{{.ButtonURL}}", "{{.URL}}", "<a "} {
		if strings.Contains(text, unknown) {
			t.Fatalf("шаблон карточки содержит %s, которого у задачи быть не должно", unknown)
		}
	}
}

func readConfig(t *testing.T) string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(Profile().LLMConfigPath)))
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func readMigrations(t *testing.T, task string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(projectRoot, "migrations", task, "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("у задачи нет своей миграции в migrations/%s", task)
	}
	var schema strings.Builder
	for _, file := range files {
		text, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		schema.Write(text)
	}
	return schema.String()
}

// stageAttachments отдаёт каталог документов каждой стадии по тексту конфигурации.
//
// Разбирать YAML целиком здесь незачем: проверяется соседство двух строк — имени стадии и её
// attachments_dir, — а стадии в файле идут ровно одним уровнем отступа.
func stageAttachments(t *testing.T) map[string]string {
	t.Helper()
	attachments := make(map[string]string)
	stage := ""
	for _, line := range strings.Split(readConfig(t), "\n") {
		if name, found := strings.CutSuffix(line, ":"); found && strings.HasPrefix(name, "    ") &&
			!strings.HasPrefix(name, "     ") {
			stage = strings.TrimSpace(name)
			continue
		}
		if directory, found := strings.CutPrefix(strings.TrimSpace(line), "attachments_dir: "); found && stage != "" {
			attachments[stage] = strings.TrimSpace(directory)
		}
	}
	return attachments
}

// Шаблон result.md печатается с Option("missingkey=error"), и опечатка в имени поля роняет
// сборку листа — уже после оплаченного прогона. Поэтому имена проверяются тестом.
//
// Источников полей два: article.ResultInput, который приходит из базы целиком, и несколько
// значений, которые дописывает сама сборка листа (result.templateData). Второй список
// приходится повторить: тип не экспортирован, а проверять шаблон задачи всё равно нужно —
// разойдётся он, и тест упадёт на первом же поле, которого не стало.
func TestResultTemplateAsksForExistingFields(t *testing.T) {
	added := map[string]struct{}{
		"Title": {}, "SEOTitle": {}, "ProfessionName": {}, "ImageName": {},
		"ImageURL": {}, "ReadingTimeMinutes": {}, "FAQItems": {}, "RelatedCourses": {},
	}
	resultInput := reflect.TypeOf(article.ResultInput{})

	raw, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(Profile().TemplatePath)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := template.New("result.md").Funcs(template.FuncMap{
		"add": func(left, right int) int { return left + right },
	}).Option("missingkey=error").Parse(string(raw)); err != nil {
		t.Fatalf("шаблон result.md не разобран: %v", err)
	}
	for _, match := range templateFieldRE.FindAllStringSubmatch(string(raw), -1) {
		name, _, _ := strings.Cut(match[1], ".")
		if _, known := added[name]; known {
			continue
		}
		if _, found := resultInput.FieldByName(name); !found {
			t.Fatalf("шаблон result.md просит поле %q, которого нет ни в article.ResultInput, "+
				"ни среди полей, которые дописывает сборка листа", name)
		}
	}
}

// templateFieldRE — обращение к полю верхнего уровня в шаблоне: «{{.Header}}», «{{.Article.Slug}}».
// Поля внутри range (например, $item.Question) сюда не попадают: у них своя область видимости.
var templateFieldRE = regexp.MustCompile(`\{\{\.([A-Za-z_][A-Za-z0-9_.]*)\}\}`)

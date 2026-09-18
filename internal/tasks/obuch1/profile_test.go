package obuch1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
)

// projectRoot — корень дерева относительно каталога пакета.
const projectRoot = "../../.."

// Ни один путь, префикс или схема задачи не должны совпадать с pprof_1: площадки разные, и
// общий каталог, общая схема или общий префикс переменных означали бы, что прогон одной
// задачи пишет в данные другой. Задачи похожи по замыслу — тем важнее, чтобы не смешались.
func TestProfileSharesNothingWithPProf1(t *testing.T) {
	own, other := Profile(), pprof1.Profile()
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
			t.Fatalf("поле профиля %s совпадает с pprof_1: %q", field.name, field.own)
		}
	}
}

// Каталоги и файлы, названные профилем, обязаны существовать в дереве проекта: иначе первая
// же команда падает на чтении шаблона или схемы стадий.
func TestProfilePathsExist(t *testing.T) {
	profile := Profile()
	for _, path := range []string{
		profile.InputDir,
		RegulationDir,
		profile.PromptsDir,
		profile.TemplatePath,
		profile.LLMConfigPath,
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

// Регламент прикреплён ровно к тем стадиям, которые решают, какой статья будет, и ни к одной
// другой.
//
// Разметке он не прикрепляется намеренно: вёрстка площадки описана в самом промпте
// 4_html.txt, и документ был бы её вторым описанием — они разошлись бы молча. Пустой каталог
// при этом роняет стадию до обращения к модели, поэтому лишний attachments_dir у html стоил
// бы прогона.
func TestRegulationIsAttachedToStructureAndExpertOnly(t *testing.T) {
	attached := stageAttachments(t)
	for _, stage := range []string{StageStructure, StageExpert} {
		if attached[stage] != RegulationDir {
			t.Fatalf("стадия %q не получает регламент: %q", stage, attached[stage])
		}
	}
	for _, stage := range []string{StageHTML, StageReview, StageInfo, StageArticle, StageKeywords} {
		if attached[stage] != "" {
			t.Fatalf("стадия %q получает документ %q, хотя не должна", stage, attached[stage])
		}
	}
}

// Документы стадий тоже свои: регламент чужой площадки описывает чужие нормы.
func TestStageAttachmentsStayInsideOwnInput(t *testing.T) {
	for stage, directory := range stageAttachments(t) {
		if !strings.HasPrefix(directory, Profile().InputDir+"/") {
			t.Fatalf("стадия %q получает документы из чужого каталога: %q", stage, directory)
		}
	}
}

// Пока у задачи нет своей папки Drive, выгрузка промптов обязана быть выключена: документ
// ищется по имени «Промт: <заголовок>», а заголовки статей совпадают с pprof_1 — прогон
// молча перезаписал бы чужой промпт.
func TestGoogleDocsStayOffWhileFolderIsUnset(t *testing.T) {
	if Profile().GoogleFolderURL != "" {
		return
	}
	if !strings.Contains(readConfig(t), "\n  google_docs: false\n") {
		t.Fatal("папка Drive не задана, а выгрузка промптов включена — она перезапишет документы pprof_1")
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
		// Промпт статьи в модель не уходит — повторять на этой стадии нечего.
		if name == StageArticle {
			continue
		}
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
func TestProfileDeclaresForeignSite(t *testing.T) {
	profile := Profile()
	if !profile.PlainBlogPages {
		t.Fatal("снят PlainBlogPages: задача получит раскладку с полями ACF, которых у площадки нет")
	}
	if profile.CommercialPages {
		t.Fatal("выставлен CommercialPages: задача пишет статьи блога, а не страницы услуг")
	}
	if !profile.WithoutSiteCatalog {
		t.Fatal("снят WithoutSiteCatalog: catalog-sync сотрёт каталог соседней площадки")
	}
	if profile.RelatedCourses {
		t.Fatal("включены RelatedCourses: каталог описывает другую площадку, под статьёй встанут чужие программы")
	}
	// Метаданные остаются: стадия info у задачи есть и пишет TL;DR с FAQ в result.md.
	// Признаки сняли бы требование метаданных с раннера, и сорванный чат 2 прошёл бы молча.
	if profile.WithoutMetadataStage || profile.MetadataFAQOnly {
		t.Fatal("у задачи со стадией info выставлен признак задачи без неё")
	}
}

// Необщие колонки обязаны быть известны репозиторию: иначе опечатка в профиле обнаружилась бы
// пустым полем в result.md, а не отказом на старте.
func TestInputColumnsAreKnownToRepository(t *testing.T) {
	if err := repository.ValidateExtraInputColumns(Profile().ExtraInputColumns); err != nil {
		t.Fatalf("колонки профиля не приняты репозиторием: %v", err)
	}
}

// Своя baseline описывает схему задачи целиком и лежит в своём каталоге: колонка, попавшая в
// чужой файл, оказалась бы в схеме соседа, и тот перестал бы стартовать на «unexpected column».
func TestOwnMigrationDescribesWholeSchema(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(projectRoot, "migrations", Name, "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("у задачи нет своей миграции в migrations/%s", Name)
	}
	var schema strings.Builder
	for _, file := range files {
		text, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		schema.Write(text)
	}
	for _, column := range Profile().ExtraInputColumns {
		if !strings.Contains(schema.String(), column+" TEXT") {
			t.Fatalf("миграция задачи не заводит колонку %q", column)
		}
	}
	// TL;DR у задачи есть: стадия info его генерирует, и колонка обязана быть в схеме.
	if !strings.Contains(schema.String(), "tldr TEXT") {
		t.Fatal("схема задачи не заводит tldr, хотя стадия info его пишет")
	}
	// Колонок коммерческой страницы в схеме статьи блога быть не должно.
	for _, absent := range []string{"section TEXT", "profession TEXT", "teachers TEXT", "service_name TEXT"} {
		if strings.Contains(schema.String(), absent) {
			t.Fatalf("схема задачи заводит чужую колонку: %q", absent)
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

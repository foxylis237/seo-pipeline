package pproftemplate1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
)

// projectRoot — корень дерева относительно каталога пакета.
const projectRoot = "../../.."

// Ни один путь, префикс или схема задачи не должны совпадать с pprof_1: это два независимых
// профиля, и общий каталог или общая схема означали бы, что артефакты одной задачи ложатся
// поверх артефактов другой. Задачи похожи по замыслу — тем важнее, чтобы данные не смешались.
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
		ReferenceDir,
		filepath.Join(profile.InputDir, "regulations", "html"),
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
// задачи: промпты соседа брать нельзя, иначе правка одной задачи молча меняет другую.
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

// Эталон прикреплён ровно к тем стадиям, которые решают, как статья будет выглядеть.
//
// Редактуре он не нужен: форма к её приходу уже задана, а лишний документ в чате уводит
// правку смысла в переписывание оформления. Разметке — тоже: у неё свой регламент вёрстки.
func TestReferenceIsAttachedToStructureAndExpertOnly(t *testing.T) {
	attached := stageAttachments(t)
	for _, stage := range []string{StageStructure, StageExpertSEO} {
		if attached[stage] != ReferenceDir {
			t.Fatalf("стадия %q не получает эталон: %q", stage, attached[stage])
		}
	}
	for _, stage := range []string{StageReview, StageInfo, StageArticle, StageKeywords} {
		if attached[stage] == ReferenceDir {
			t.Fatalf("стадия %q получает эталон, хотя форму не задаёт", stage)
		}
	}
	if attached[StageHTML] == ReferenceDir {
		t.Fatal("стадия html получает эталон вместо регламента вёрстки")
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
	// Колонок коммерческих страниц в схеме статьи блога быть не должно.
	for _, absent := range []string{"service_name TEXT", "teachers TEXT", "seo_title TEXT"} {
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

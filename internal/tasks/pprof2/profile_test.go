package pprof2

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
)

// Ни один путь, префикс или схема pprof_2 не должны совпадать с pprof_1: это два независимых
// профиля, и общий каталог или общая схема означали бы, что данные одной задачи попадают в
// другую. Проверяется именно совпадение значений, а не их вид — переименование каталога
// законно, а совпадение с чужим нет.
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
	// Папка Drive тоже своя: документ ищется по имени «Промт: <заголовок>», а заголовки
	// страниц разных задач совпадают.
	if own.GoogleFolderURL == "" {
		t.Fatal("папка Google Drive у pprof_2 не задана, документы уйдут в общую")
	}
	if own.GoogleFolderURL == other.GoogleFolderURL {
		t.Fatalf("папка Google Drive совпадает с pprof_1: %q", own.GoogleFolderURL)
	}
}

// Каталоги и файлы, названные профилем, обязаны существовать в дереве проекта: иначе первая
// же команда падает на чтении шаблона или схемы стадий.
func TestProfilePathsExist(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	profile := Profile()
	for _, path := range []string{
		profile.InputDir,
		filepath.Join(profile.InputDir, "images"),
		filepath.Join(profile.InputDir, "regulations", "html"),
		profile.PromptsDir,
		profile.TemplatePath,
		profile.LLMConfigPath,
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Fatalf("путь профиля %s недоступен: %v", path, err)
		}
	}
}

// Каждая стадия схемы обязана иметь свой файл промпта, и все файлы — лежать в каталоге
// pprof_2: промпты чужой задачи брать нельзя, иначе правка одной молча меняет другую.
func TestEveryStageHasItsOwnPrompt(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	config, err := os.ReadFile(filepath.Join(root, "config", "pprof_2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	for _, stage := range Stages {
		if !strings.Contains(text, "\n    "+stage+":\n") {
			t.Fatalf("стадия %q не описана в config/pprof_2.yaml", stage)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		prompt, found := strings.CutPrefix(trimmed, "prompt: ")
		if !found {
			continue
		}
		if !strings.HasPrefix(prompt, Profile().PromptsDir+"/") {
			t.Fatalf("промпт %q лежит вне каталога задачи", prompt)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(prompt))); err != nil {
			t.Fatalf("промпт %s недоступен: %v", prompt, err)
		}
	}
}

// Необщие колонки обязаны быть известны репозиторию: иначе опечатка в профиле обнаружилась бы
// пустым полем в result.md, а не отказом на старте.
func TestInputColumnsAreKnownToRepository(t *testing.T) {
	if err := repository.ValidateExtraInputColumns(InputColumns); err != nil {
		t.Fatalf("колонки профиля не приняты репозиторием: %v", err)
	}
}

// Задачи не должны заимствовать колонки друг у друга: набор pprof_2 и набор задач, пишущих
// статьи блога, пересекаться не имеют права. Проверка ловит обе стороны — и свою колонку,
// уехавшую к соседу, и чужую, приехавшую сюда.
func TestInputColumnsDoNotOverlapWithBlogTasks(t *testing.T) {
	blog := pprof1.Profile().ExtraInputColumns
	if len(blog) == 0 {
		t.Fatal("pprof_1 перестал объявлять свои колонки: перелинковка, профессии и метки нужны ему")
	}
	for _, own := range InputColumns {
		if slices.Contains(blog, own) {
			t.Fatalf("колонка %q объявлена и у pprof_2, и у pprof_1", own)
		}
	}
	for _, foreign := range blog {
		if slices.Contains(InputColumns, foreign) {
			t.Fatalf("колонка %q статей блога объявлена у pprof_2", foreign)
		}
	}
}

// Своя baseline описывает схему задачи целиком: и то, что у неё есть, и то, чего у неё нет.
//
// Лежать она обязана в своём каталоге: у каждой задачи он свой, и колонка pprof_2, попавшая
// в чужой файл, оказалась бы в схеме task_1 или pprof_1 — обе перестали бы стартовать на
// «unexpected column».
func TestOwnMigrationDescribesWholeSchema(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	own, err := filepath.Glob(filepath.Join(root, "migrations", "pprof_2", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var migrations strings.Builder
	for _, name := range own {
		text, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		migrations.Write(text)
	}
	schema := migrations.String()
	for _, column := range InputColumns {
		if !strings.Contains(schema, column+" TEXT") {
			t.Fatalf("миграции pprof_2 не заводят колонку %q", column)
		}
	}
	// Колонок статей блога и TL;DR в схеме pprof_2 быть не должно: перелинковки у задачи нет,
	// похожих профессий она не печатает, меток не публикует, TL;DR не генерирует.
	for _, absent := range []string{"links TEXT", "professions TEXT", "tags TEXT", "tldr TEXT"} {
		if strings.Contains(schema, absent) {
			t.Fatalf("схема pprof_2 заводит колонку, которой у задачи нет: %q", absent)
		}
	}

	foreign, err := filepath.Glob(filepath.Join(root, "migrations", "*", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range foreign {
		if filepath.Base(filepath.Dir(name)) == "pprof_2" {
			continue
		}
		text, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, column := range InputColumns {
			if strings.Contains(string(text), " "+column+" TEXT") {
				t.Fatalf("миграция %s/%s заводит колонку %q задачи pprof_2",
					filepath.Base(filepath.Dir(name)), filepath.Base(name), column)
			}
		}
	}
}

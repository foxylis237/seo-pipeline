package pprof1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
)

// Бюджет стадии обязан вмещать не одну попытку: отказ провайдера лечится повтором, а повтор
// не начнётся, пока первая попытка забирает бюджет целиком. Проверяется по живому файлу
// задачи — расхождение здесь и есть стадия, которая падает, ни разу не повторившись.
func TestStageBudgetsLeaveRoomForRetries(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
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
		// Перегруженного провайдера пережидают минуту и три: без этого запаса повторы
		// упрутся в конец бюджета раньше, чем отказ успеет пройти.
		if spare := stage.Timeout - stage.AttemptTimeout; spare < 4*time.Minute {
			t.Fatalf("стадия %s: на паузы между повторами остаётся %v", name, spare)
		}
	}
}

// Схема pprof_1 описана своим файлом целиком, и он обязан заводить ровно те необязательные
// колонки, которые объявил профиль.
//
// Проверка нужна потому, что расхождение здесь не видно ниоткуда: ValidateSchema сравнивает
// профиль с живой базой, а не с файлом, и недостающая в миграции колонка обнаружится только
// на новой установке — то есть у того, кто разворачивает проект с нуля.
func TestOwnMigrationMatchesProfileColumns(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	columns := Profile().ExtraInputColumns
	if err := repository.ValidateExtraInputColumns(columns); err != nil {
		t.Fatalf("колонки профиля не приняты репозиторием: %v", err)
	}
	own, err := filepath.Glob(filepath.Join(root, "migrations", "pprof_1", "*.up.sql"))
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
	if schema == "" {
		t.Fatal("у pprof_1 нет собственных миграций: схема задачи должна описываться своим каталогом")
	}
	for _, column := range columns {
		if !strings.Contains(schema, column+" TEXT") {
			t.Fatalf("миграции pprof_1 не заводят колонку %q", column)
		}
	}
	// Колонок pprof_2 в этой схеме быть не должно: поля одной задачи не имеют права
	// появляться в таблицах другой, и проверка схемы строга в обе стороны.
	for _, foreign := range []string{"seo_title", "section", "profession", "teachers", "service_name"} {
		if strings.Contains(schema, foreign+" TEXT") {
			t.Fatalf("схема pprof_1 заводит колонку %q задачи pprof_2", foreign)
		}
	}
	// TL;DR у pprof_1 есть: стадия info его генерирует, и колонка обязана быть в схеме.
	if !strings.Contains(schema, "tldr TEXT") {
		t.Fatal("схема pprof_1 не заводит tldr, хотя стадия info его пишет")
	}
}

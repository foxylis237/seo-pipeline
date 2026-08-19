package repository

import (
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Опечатка в профиле обязана ронять команду на старте: колонка, которой нет в реестре, не
// будет ни записана, ни прочитана, и обнаружилось бы это пустым полем в result.md.
func TestValidateExtraInputColumnsRejectsUnknownName(t *testing.T) {
	err := ValidateExtraInputColumns([]string{"seo_titel"})
	if err == nil {
		t.Fatal("неизвестная колонка принята")
	}
	if !strings.Contains(err.Error(), "seo_title") {
		t.Fatalf("сообщение не подсказывает известные колонки: %v", err)
	}
}

// Базовая колонка не может быть объявлена необщей: она есть у всех задач, и повторное
// объявление дало бы её дважды в списке INSERT.
func TestValidateExtraInputColumnsRejectsBaseColumn(t *testing.T) {
	if err := ValidateExtraInputColumns([]string{"category"}); err == nil {
		t.Fatal("базовая колонка принята как необщая")
	}
}

func TestValidateExtraInputColumnsRejectsDuplicate(t *testing.T) {
	if err := ValidateExtraInputColumns([]string{"section", "section"}); err == nil {
		t.Fatal("повторное объявление колонки принято")
	}
}

// Пустой список — это задача без своих колонок, и она обязана оставаться законной: так живут
// task_1 и pprof_1.
func TestValidateExtraInputColumnsAcceptsEmpty(t *testing.T) {
	if err := ValidateExtraInputColumns(nil); err != nil {
		t.Fatalf("задача без своих колонок отклонена: %v", err)
	}
}

// Задача без своих колонок обязана давать в точности прежний запрос: список колонок,
// значения и проекция result.md не должны отличаться от тех, что были до появления механизма.
func TestTaskWithoutOwnColumnsBuildsBaseQueryOnly(t *testing.T) {
	repository := &ArticleRepository{}
	columns, values := repository.insertInputColumns(article.Input{Category: "рубрика"})
	if len(columns) != len(baseInputColumns) {
		t.Fatalf("колонок %d, ожидалось %d: %v", len(columns), len(baseInputColumns), columns)
	}
	if len(values) != len(columns) {
		t.Fatalf("значений %d при %d колонках", len(values), len(columns))
	}
	var result article.ResultInput
	if projection, targets := repository.resultInputProjection(&result); projection != "" || targets != nil {
		t.Fatalf("выборка result.md изменилась: %q, %v", projection, targets)
	}
}

// Свои колонки дописываются в конец и в объявленном порядке, а значения идут с ними в паре:
// разъехавшийся порядок разложил бы данные по чужим колонкам молча.
func TestOwnColumnsAreAppendedInOrder(t *testing.T) {
	repository := &ArticleRepository{}
	if err := repository.UseExtraInputColumns([]string{"seo_title", "section", "profession", "teachers"}); err != nil {
		t.Fatal(err)
	}
	columns, values := repository.insertInputColumns(article.Input{
		Category: "рубрика", SEOTitle: "сео", Section: "раздел",
		Profession: "стропальщик", Teachers: "Иванов",
	})
	own := columns[len(baseInputColumns):]
	want := []string{"seo_title", "section", "profession", "teachers"}
	if strings.Join(own, ",") != strings.Join(want, ",") {
		t.Fatalf("свои колонки %v, ожидались %v", own, want)
	}
	ownValues := values[len(baseInputColumns):]
	for index, expected := range []string{"сео", "раздел", "стропальщик", "Иванов"} {
		if ownValues[index] != expected {
			t.Fatalf("значение колонки %s = %v, ожидалось %q", own[index], ownValues[index], expected)
		}
	}
}

// Проекция result.md обязана читать те же колонки в том же порядке и класть их в свои поля.
func TestResultProjectionReadsOwnColumns(t *testing.T) {
	repository := &ArticleRepository{}
	if err := repository.UseExtraInputColumns([]string{"seo_title", "section", "profession", "teachers"}); err != nil {
		t.Fatal(err)
	}
	var result article.ResultInput
	projection, targets := repository.resultInputProjection(&result)
	for _, column := range []string{"seo_title", "section", "profession", "teachers"} {
		if !strings.Contains(projection, "COALESCE(i."+column+", '')") {
			t.Fatalf("колонка %q не читается: %q", column, projection)
		}
	}
	if len(targets) != 4 {
		t.Fatalf("целей сканирования %d, ожидалось 4", len(targets))
	}
	for index, value := range []string{"сео", "раздел", "стропальщик", "Иванов"} {
		target, ok := targets[index].(*string)
		if !ok {
			t.Fatalf("цель %d не строковая", index)
		}
		*target = value
	}
	if result.SEOTitle != "сео" || result.Section != "раздел" ||
		result.Profession != "стропальщик" || result.Teachers != "Иванов" {
		t.Fatalf("значения разложены не по своим полям: %+v", result)
	}
}

// Плейсхолдеры нумеруются подряд с заданного места: сдвиг на единицу отправил бы каждое
// значение в соседнюю колонку.
func TestPlaceholdersAreNumberedFromOffset(t *testing.T) {
	if got := placeholders(3, 4); got != "$3, $4, $5, $6" {
		t.Fatalf("placeholders(3, 4) = %q", got)
	}
	if got := excludedAssignments([]string{"a", "b"}); got != "a = EXCLUDED.a, b = EXCLUDED.b" {
		t.Fatalf("excludedAssignments = %q", got)
	}
}

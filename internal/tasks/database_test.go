package tasks

import (
	"strings"
	"testing"
)

// task_1 живёт в public, и DSN ему возвращается нетронутым: любая нормализация строки здесь
// была бы изменением поведения работающей задачи.
func TestDatabaseURLLeavesPublicSchemaUntouched(t *testing.T) {
	profile := Profile{Name: "task_1", DBSchema: "public"}
	const base = "postgres://seo:secret@localhost:5432/seo?sslmode=disable"
	got, err := profile.DatabaseURL(base)
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Fatalf("DatabaseURL(task_1) = %q, want %q без изменений", got, base)
	}
}

func TestDatabaseURLRoutesTaskToItsSchema(t *testing.T) {
	profile := Profile{Name: "pprof_1", DBSchema: "pprof_1"}
	got, err := profile.DatabaseURL("postgres://seo:secret@localhost:5432/seo?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "search_path=pprof_1") {
		t.Fatalf("DatabaseURL(pprof_1) = %q, без search_path", got)
	}
	if !strings.Contains(got, "sslmode=disable") {
		t.Fatalf("DatabaseURL(pprof_1) = %q, потерян существующий параметр", got)
	}
}

// Существующий search_path перезаписывается: изоляция задачи обязана держаться на коде, а не
// на том, что написано в .env.
func TestDatabaseURLOverridesExistingSearchPath(t *testing.T) {
	profile := Profile{Name: "pprof_1", DBSchema: "pprof_1"}
	got, err := profile.DatabaseURL("postgres://seo:secret@localhost:5432/seo?search_path=public")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "search_path=public") {
		t.Fatalf("DatabaseURL(pprof_1) = %q, чужая схема осталась", got)
	}
	if !strings.Contains(got, "search_path=pprof_1") {
		t.Fatalf("DatabaseURL(pprof_1) = %q, своя схема не подставлена", got)
	}
}

// pgx принимает и keyword/value форму. Молча уронить её в public нельзя.
func TestDatabaseURLSupportsKeywordValueDSN(t *testing.T) {
	profile := Profile{Name: "pprof_1", DBSchema: "pprof_1"}
	got, err := profile.DatabaseURL("host=localhost user=seo dbname=seo")
	if err != nil {
		t.Fatal(err)
	}
	if got != "host=localhost user=seo dbname=seo search_path=pprof_1" {
		t.Fatalf("DatabaseURL(keyword/value) = %q", got)
	}
}

func TestDatabaseURLRejectsEmptyBase(t *testing.T) {
	profile := Profile{Name: "pprof_1", DBSchema: "pprof_1"}
	if _, err := profile.DatabaseURL("   "); err == nil {
		t.Fatal("пустой DSN принят")
	}
}

// Имя схемы проверяется до подстановки в DSN: в строку подключения не должно попадать ничего,
// что требует кавычек или экранирования.
func TestValidateSchemaNameRejectsUnsafeIdentifiers(t *testing.T) {
	unsafe := []string{
		"",
		"Public",
		"pprof-1",
		"pprof 1",
		"public;drop",
		"public search_path=other",
		`"public"`,
		"1task",
		strings.Repeat("a", maxSchemaNameLength+1),
	}
	for _, schema := range unsafe {
		if err := validateSchemaName(schema); err == nil {
			t.Fatalf("validateSchemaName(%q) принял небезопасное имя", schema)
		}
	}
	for _, schema := range []string{"public", "pprof_1", "_internal", "t1"} {
		if err := validateSchemaName(schema); err != nil {
			t.Fatalf("validateSchemaName(%q) отклонил корректное имя: %v", schema, err)
		}
	}
}

func TestDatabaseURLRejectsInvalidSchemaBeforeBuildingDSN(t *testing.T) {
	profile := Profile{Name: "broken", DBSchema: "pprof-1; drop schema public"}
	if _, err := profile.DatabaseURL("postgres://seo@localhost:5432/seo"); err == nil {
		t.Fatal("небезопасная схема попала в DSN")
	}
}

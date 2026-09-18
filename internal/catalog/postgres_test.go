package catalog

import "testing"

// Схема каталога — обязательный аргумент, и умолчания у него нет намеренно. Replace
// начинается с удаления всех услуг: «если не сказано, пишем в site» означало бы, что
// забытая настройка новой задачи стирает каталог соседней площадки.
func TestNewPostgresStoreRequiresSchema(t *testing.T) {
	for _, schema := range []string{"", "   "} {
		store, err := NewPostgresStore(nil, schema)
		if err == nil {
			t.Fatalf("схема %q принята, хранилище = %v", schema, store)
		}
		if store != nil {
			t.Fatal("при отказе вернулось хранилище")
		}
	}
}

// Имя схемы подставляется в текст запроса, а не уходит параметром, — так схему назвать
// нельзя. Значит оно обязано пройти через приведение идентификатора.
func TestPostgresStoreQualifiesTables(t *testing.T) {
	store, err := NewPostgresStore(nil, "site_obuchim")
	if err != nil {
		t.Fatal(err)
	}
	if got := store.table("programs"); got != `"site_obuchim".programs` {
		t.Fatalf("имя таблицы = %s", got)
	}
	// Кавычка в имени схемы обязана остаться внутри идентификатора, а не разорвать запрос.
	tricky, err := NewPostgresStore(nil, `site"; DROP SCHEMA site; --`)
	if err != nil {
		t.Fatal(err)
	}
	if got := tricky.table("programs"); got == `site"; DROP SCHEMA site; --.programs` {
		t.Fatalf("имя схемы попало в запрос как есть: %s", got)
	}
}

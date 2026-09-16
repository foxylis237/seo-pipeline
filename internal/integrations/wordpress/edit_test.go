package wordpress

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// updateRequestBody снимает то, что правка действительно отправила площадке.
func updateRequestBody(t *testing.T, update PostUpdate) string {
	t.Helper()
	var body string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, methodResponse(`<value><boolean>1</boolean></value>`))
	})
	if err := client.EditPost(context.Background(), update); err != nil {
		t.Fatalf("переписать запись: %v", err)
	}
	return body
}

func relationUpdate(field FieldUpdate) PostUpdate {
	return PostUpdate{
		PostID: 22615, Title: "Разряды газосварщиков", ContentHTML: articleHTML,
		Fields: []FieldUpdate{field},
	}
}

// Связь уходит массивом идентификаторов, а не строкой: готовую сериализованную строку
// WordPress пропускает через maybe_serialize второй раз, и связь перестаёт читаться.
func TestEditPostSendsRelationAsArray(t *testing.T) {
	body := updateRequestBody(t, relationUpdate(FieldUpdate{
		ID: "88123", Key: "related_courses", IDs: []int64{507, 15941},
	}))
	if !strings.Contains(body, "related_courses") {
		t.Fatalf("поле не ушло в запрос: %s", body)
	}
	for _, id := range []string{"507", "15941"} {
		if !strings.Contains(body, "<string>"+id+"</string>") {
			t.Fatalf("идентификатор %s ушёл не отдельным значением: %s", id, body)
		}
	}
	if strings.Contains(body, "a:2:{") {
		t.Fatalf("связь ушла сериализованной строкой: %s", body)
	}
	if !strings.Contains(body, "88123") {
		t.Fatalf("правка ушла без идентификатора postmeta — WordPress завёл бы второе поле: %s", body)
	}
}

// Поле, которого у записи ещё нет, заводится правкой: id не отправляется вовсе. Так связь
// related_courses попадает в статьи, опубликованные до появления блока курсов.
func TestEditPostCreatesFieldWithoutID(t *testing.T) {
	body := updateRequestBody(t, relationUpdate(FieldUpdate{
		Key: "related_courses", IDs: []int64{507}, Create: true,
	}))
	if strings.Contains(body, "<name>id</name>") {
		t.Fatalf("у нового поля ушёл пустой идентификатор: %s", body)
	}
	if !strings.Contains(body, "related_courses") {
		t.Fatalf("новое поле не ушло в запрос: %s", body)
	}
}

// Без явного разрешения пустой идентификатор — отказ, и отказ до запроса: правка завела бы
// рядом второе поле с тем же ключом, и какое из двух достанется get_field(), решал бы
// порядок в базе.
func TestEditPostRefusesFieldWithoutIDAndCreate(t *testing.T) {
	var called bool
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, methodResponse(`<value><boolean>1</boolean></value>`))
	})
	err := client.EditPost(context.Background(), relationUpdate(FieldUpdate{
		Key: "prof_title", Value: "Заголовок",
	}))
	if err == nil {
		t.Fatal("поле без идентификатора принято")
	}
	if !strings.Contains(err.Error(), "prof_title") {
		t.Fatalf("ошибка не называет поле: %v", err)
	}
	if called {
		t.Fatal("запрос ушёл в блог до проверки")
	}
}

// Сверка связи идёт составом идентификаторов: WordPress возвращает её сериализованным
// массивом PHP, и порядок внутри связи не значим.
func TestVerifyFieldsComparesRelationByIDs(t *testing.T) {
	stored := StoredPost{Fields: map[string]string{
		"related_courses": `a:2:{i:0;s:5:"15941";i:1;s:3:"507";}`,
	}}
	updates := []FieldUpdate{{ID: "1", Key: "related_courses", IDs: []int64{507, 15941}}}
	if mismatches := VerifyFields(updates, stored); len(mismatches) > 0 {
		t.Fatalf("совпавшая связь сочтена расхождением: %v", mismatches)
	}
}

// Молча отброшенный ключ — то, ради чего сверка и нужна: ответ «200 OK» о содержимом
// postmeta не говорит ничего.
func TestVerifyFieldsCatchesDroppedField(t *testing.T) {
	updates := []FieldUpdate{{ID: "1", Key: "related_courses", IDs: []int64{507}}}
	mismatches := VerifyFields(updates, StoredPost{Fields: map[string]string{}})
	if len(mismatches) != 1 || mismatches[0].Field != "related_courses" {
		t.Fatalf("отброшенное поле не поймано: %v", mismatches)
	}
}

package llm

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
)

// attachingClient — провайдер, умеющий отправить документ вместе с промптом.
type attachingClient struct{ fakeClient }

func (c *attachingClient) SupportsAttachments() bool { return true }

func attachmentRouter(t *testing.T, client Client, directory string) *Router {
	t.Helper()
	temperature := 0.1
	router := NewRouter(config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{"selected": {Type: "deepseek_web"}},
		Stages: map[string]config.LLMStageConfig{"html": {
			Targets:        []config.LLMTargetConfig{{Provider: "selected", Model: "deepseek-web"}},
			PromptTemplate: "Разметь статью {{.Title}}",
			Temperature:    &temperature, MaxTokens: 100, Timeout: time.Second,
			AttachmentsDir: directory,
			Mode:           "Быстрый",
		}},
	}, map[string]Client{"selected": client}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router.sleep = func(context.Context, time.Duration) error { return nil }
	return router
}

// regulationDir — каталог с единственным документом стадии, как его ждёт конфигурация.
func regulationDir(t *testing.T) (directory, document string) {
	t.Helper()
	directory = t.TempDir()
	document = filepath.Join(directory, "Регламент вёрстки.pdf")
	if err := os.WriteFile(document, []byte("%PDF"), 0o600); err != nil {
		t.Fatalf("подготовить документ стадии: %v", err)
	}
	return directory, document
}

func TestRouterPassesStageAttachmentsAndMode(t *testing.T) {
	directory, document := regulationDir(t)
	client := &attachingClient{}
	router := attachmentRouter(t, client, directory)

	if _, err := router.Generate(context.Background(), Call{
		Stage: "html", ArticleID: 7, Data: struct{ Title string }{"Тема"},
	}); err != nil {
		t.Fatalf("стадия с документом: %v", err)
	}
	if got := strings.Join(client.request.Attachments, ","); got != document {
		t.Fatalf("документы стадии не дошли до провайдера: %q", got)
	}
	if client.request.Mode != "Быстрый" {
		t.Fatalf("режим стадии не дошёл до провайдера: %q", client.request.Mode)
	}
}

// Провайдер без поддержки документов не имеет права получить стадию с регламентом: промпт
// уйдёт, ответ придёт, и подмену будет уже не заметить.
func TestRouterRejectsAttachmentsForProviderWithoutSupport(t *testing.T) {
	directory, _ := regulationDir(t)
	client := &fakeClient{}
	router := attachmentRouter(t, client, directory)

	_, err := router.Generate(context.Background(), Call{
		Stage: "html", ArticleID: 7, Data: struct{ Title string }{"Тема"},
	})
	if err == nil {
		t.Fatal("стадия с документом ушла провайдеру, который их не поддерживает")
	}
	if !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("ошибка не называет причину: %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("провайдер был вызван %d раз(а) вместо отказа маршрутизации", client.calls)
	}
}

// Ненайденный документ обязан остановить стадию до обращения к модели: промпт написан в
// расчёте на регламент, и ответ без него внешне неотличим от правильного.
func TestRouterStopsStageWhenDocumentIsMissing(t *testing.T) {
	client := &attachingClient{}
	router := attachmentRouter(t, client, filepath.Join(t.TempDir(), "нет-каталога"))

	_, err := router.Generate(context.Background(), Call{
		Stage: "html", ArticleID: 7, Data: struct{ Title string }{"Тема"},
	})
	if err == nil {
		t.Fatal("стадия ушла в модель без документа")
	}
	if client.calls != 0 {
		t.Fatalf("провайдер был вызван %d раз(а) без документа", client.calls)
	}
	if !strings.Contains(err.Error(), "нет-каталога") {
		t.Fatalf("ошибка не называет каталог документа: %v", err)
	}
}

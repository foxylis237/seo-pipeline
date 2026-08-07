package deepseekweb

import (
	"io"
	"log/slog"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

func chatClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient(Config{
		ChatURL: "https://chat.deepseek.com/", LoginURL: "https://chat.deepseek.com/sign_in",
		ProfileDir: t.TempDir(),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// Режим одного диалога — свойство запроса, а не клиента: один и тот же браузерный клиент
// обслуживает обе схемы стадий, и в схеме Gemini этот режим не нужен.
func request(articleID int64, singleChat bool) llm.Request {
	return llm.Request{Prompt: "prompt", ArticleID: articleID, SingleChat: singleChat}
}

func TestNewChatForEveryRequestWithoutSingleChatMode(t *testing.T) {
	client := chatClient(t)
	for range 3 {
		if !client.shouldOpenNewChat(request(7, false)) {
			t.Fatal("обычный режим обязан открывать новую беседу на каждый запрос")
		}
		client.markChatOpened(7)
	}
}

func TestSingleChatIsReusedWithinArticle(t *testing.T) {
	client := chatClient(t)
	if !client.shouldOpenNewChat(request(7, true)) {
		t.Fatal("первая стадия статьи обязана открыть беседу")
	}
	client.markChatOpened(7)
	for stage := 2; stage <= 6; stage++ {
		if client.shouldOpenNewChat(request(7, true)) {
			t.Fatalf("стадия %d открыла новую беседу вместо продолжения", stage)
		}
	}
}

func TestSingleChatRestartsOnNewArticle(t *testing.T) {
	client := chatClient(t)
	client.markChatOpened(7)
	if client.shouldOpenNewChat(request(7, true)) {
		t.Fatal("та же статья не должна открывать новую беседу")
	}
	if !client.shouldOpenNewChat(request(8, true)) {
		t.Fatal("смена статьи обязана открыть новую беседу")
	}
}

// Один клиент обслуживает обе схемы: запрос из схемы Gemini не должен переиспользовать
// беседу, открытую запросом DeepSeek-only режима.
func TestSingleChatIsIgnoredForRequestsWithoutTheFlag(t *testing.T) {
	client := chatClient(t)
	client.markChatOpened(7)
	if !client.shouldOpenNewChat(request(7, false)) {
		t.Fatal("запрос без флага переиспользовал чужую беседу")
	}
}

func TestSingleChatStartsFreshAfterSessionReset(t *testing.T) {
	client := chatClient(t)
	client.markChatOpened(7)
	client.session = nil
	if err := client.resetSession(); err != nil {
		t.Fatal(err)
	}
	if !client.shouldOpenNewChat(request(7, true)) {
		t.Fatal("после потери сессии беседу нужно открывать заново")
	}
}

func TestSingleChatFallsBackToNewChatWithoutArticleID(t *testing.T) {
	client := chatClient(t)
	// ArticleID = 0 приходит от вызовов вне пайплайна: привязывать к нему беседу нельзя,
	// иначе разные статьи склеятся в один диалог.
	if !client.shouldOpenNewChat(request(0, true)) {
		t.Fatal("запрос без article_id обязан открывать новую беседу")
	}
	client.markChatOpened(0)
	if !client.shouldOpenNewChat(request(0, true)) {
		t.Fatal("запросы без article_id не должны переиспользовать беседу")
	}
}

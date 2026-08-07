package main

import (
	"context"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// Заглушка обязана отвечать на каждую стадию пайплайна. Стадии article и info в ней
// отсутствовали, и это не всплывало: офлайн-прогон падал раньше, на подмене модели.
func TestDryRunClientProvidesEveryStatelessStage(t *testing.T) {
	client := dryRunClient{}
	for _, stage := range pipelineStageOrder {
		response, err := client.Generate(context.Background(), llm.Request{Prompt: "rendered prompt", Model: dryRunModelPrefix + stage})
		if err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
		if strings.TrimSpace(response.Text) == "" || response.Model != dryRunModelPrefix+stage {
			t.Fatalf("stage %s response = %+v", stage, response)
		}
	}
}

func TestDryRunChatProvidesReviewAndFixedArticle(t *testing.T) {
	// Чат dry-run повторяет продовую схему: первое сообщение — review, второе — fix.
	chat, err := (dryRunClient{}).NewChat(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer chat.Close()
	reviewResponse, err := chat.Generate(context.Background(), "review prompt")
	if err != nil || strings.TrimSpace(reviewResponse.Text) == "" {
		t.Fatalf("review response = %+v, %v", reviewResponse, err)
	}
	fixResponse, err := chat.Generate(context.Background(), "fix prompt")
	if err != nil || !strings.Contains(fixResponse.Text, "[[ARTICLE_COMPLETE]]") {
		t.Fatalf("fix response = %+v, %v", fixResponse, err)
	}
}

func TestDryRunChatWithHistoryStartsFromFixMessage(t *testing.T) {
	chat, err := (dryRunClient{}).NewChatWithHistory(context.Background(), 7,
		llm.Message{Role: "user", Content: "review prompt"},
		llm.Message{Role: "assistant", Content: "review answer"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer chat.Close()
	response, err := chat.Generate(context.Background(), "fix prompt")
	if err != nil || !strings.Contains(response.Text, "[[ARTICLE_COMPLETE]]") {
		t.Fatalf("fix response = %+v, %v", response, err)
	}
}

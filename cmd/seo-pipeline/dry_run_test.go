package main

import (
	"context"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
)

func TestDryRunClientProvidesEveryStatelessStage(t *testing.T) {
	client := dryRunClient{}
	for _, stage := range []string{"structure", "review", "fix", "html"} {
		response, err := client.Generate(context.Background(), llm.Request{Prompt: "rendered prompt", Model: dryRunModelPrefix + stage})
		if err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
		if strings.TrimSpace(response.Text) == "" || response.Model != dryRunModelPrefix+stage {
			t.Fatalf("stage %s response = %+v", stage, response)
		}
	}
}

func TestDryRunChatProvidesArticleAndParseableInfo(t *testing.T) {
	chat, err := (dryRunClient{}).NewChat(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer chat.Close()
	articleResponse, err := chat.Generate(context.Background(), "article prompt")
	if err != nil || !strings.Contains(articleResponse.Text, "[[ARTICLE_COMPLETE]]") {
		t.Fatalf("article response = %+v, %v", articleResponse, err)
	}
	infoResponse, err := chat.Generate(context.Background(), "info prompt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := article.ParseArticleInfo(infoResponse.Text); err != nil {
		t.Fatalf("parse dry-run info: %v", err)
	}
}

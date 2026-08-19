package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof2"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1"
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

// Заглушка стадии info обязана соответствовать контракту стадии: только TL;DR и FAQ.
// Секция «Метки» в ней переводила разбор в нестрогий режим и уезжала в допинфо, а dry-run
// существует ровно для того, чтобы такие расхождения ловились без внешних сервисов.
func TestDryRunInfoStubMatchesStageContract(t *testing.T) {
	info, err := article.ParseArticleInfo(dryRunInfo)
	if err != nil {
		t.Fatalf("заглушка info не разобрана: %v", err)
	}
	if info.FallbackUsed {
		t.Fatalf("заглушка info разобрана нестрогим разбором: %+v", info)
	}
	if strings.TrimSpace(info.TLDR) == "" || strings.TrimSpace(info.FAQ) == "" {
		t.Fatalf("заглушка info неполна: %+v", info)
	}
	if strings.TrimSpace(info.AdditionalInfo) != "" {
		t.Fatalf("заглушка info содержит лишние разделы: %q", info.AdditionalInfo)
	}
}

// Стадию заглушка обязана брать из запроса, а не из имени модели: в чате все сообщения после
// первого уходят к target, ответившему на первое, и модель у них общая. Пока стадию читали из
// модели, info у pprof_1 получала текст статьи, разбор метаданных молча проваливался, и
// офлайн-прогон собирал result.md с пустыми TL;DR и FAQ.
func TestDryRunClientAnswersByRequestStage(t *testing.T) {
	client := dryRunClient{}
	response, err := client.Generate(context.Background(), llm.Request{
		Prompt: "rendered prompt",
		// Модель чата — та, что ответила на первое сообщение, а стадия уже другая.
		Model: dryRunModelPrefix + "expert",
		Stage: "info",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := article.ParseArticleInfo(response.Text)
	if err != nil {
		t.Fatalf("ответ стадии info не разобран: %v", err)
	}
	if strings.TrimSpace(info.TLDR) == "" || strings.TrimSpace(info.FAQ) == "" {
		t.Fatalf("ответ стадии info неполон: %+v", info)
	}
}

// У задачи без стадии fix готовый текст возвращает сама review. Заглушка с замечаниями
// положила бы их в fixed_article.txt вместо статьи, и прогон «прошёл бы» с мусором.
func TestDryRunReviewAnswerDependsOnFixStage(t *testing.T) {
	withFix := dryRunStageResponses(false)["review"]
	if strings.Contains(withFix, "[[ARTICLE_COMPLETE]]") {
		t.Fatalf("review задачи со стадией fix вернула текст статьи: %q", withFix)
	}
	withoutFix := dryRunStageResponses(true)["review"]
	if !strings.Contains(withoutFix, "[[ARTICLE_COMPLETE]]") {
		t.Fatalf("review задачи без стадии fix не вернула текст статьи: %q", withoutFix)
	}
}

// Стадия без ответа роняет офлайн-прогон в середине. Проверяются профили всех задач: набор
// стадий у них разный, и новая стадия задачи обязана появиться в заглушке вместе с собой.
func TestDryRunStageResponsesCoverEveryTaskStage(t *testing.T) {
	for _, profile := range []tasks.Profile{task1.Profile(), pprof1.Profile(), pprof2.Profile()} {
		responses := dryRunStageResponses(!slices.Contains(profile.LLMStages, string(stageFix)))
		for _, stage := range profile.LLMStages {
			if strings.TrimSpace(responses[stage]) == "" {
				t.Fatalf("задача %s: у стадии %q нет ответа заглушки", profile.Name, stage)
			}
		}
	}
}

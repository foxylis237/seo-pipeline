// Package generation defines the article text generation boundary.
package generation

import (
	"context"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// ChatFactory creates an isolated conversation for one article. The article id travels with
// the chat so that every LLM request it makes is attributable to the article in the logs.
type ChatFactory interface {
	NewChat(ctx context.Context, articleID int64) (llm.Chat, error)
	// NewChatWithHistory продолжает чат стадии, выполненной в прошлом прогоне: история
	// восстанавливается из сохранённых артефактов, без повторного запроса к модели.
	NewChatWithHistory(ctx context.Context, articleID int64, history ...llm.Message) (llm.Chat, error)
}

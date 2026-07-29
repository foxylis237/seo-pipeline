// Package generation defines the article text generation boundary.
package generation

import (
	"context"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// ChatFactory creates an isolated conversation for one article.
type ChatFactory interface {
	NewChat(ctx context.Context) (llm.Chat, error)
}

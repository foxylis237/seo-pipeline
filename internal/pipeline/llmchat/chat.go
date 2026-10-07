// Package llmchat даёт поток сообщений одного диалога поверх роутера LLM; о задачах и
// порядке их стадий он не знает.
package llmchat

import (
	"context"
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// RouterChats открывает изолированные диалоги поверх существующего роутера.
type RouterChats struct{ router *llm.Router }

// NewRouterChats собирает фабрику чатов поверх роутера.
func NewRouterChats(router *llm.Router) RouterChats { return RouterChats{router: router} }

// NewChat открывает новый диалог по списку стадий, а не продолжает предыдущий.
func (c RouterChats) NewChat(ctx context.Context, articleID int64, stages ...string) (*Chat, error) {
	if c.router == nil {
		return nil, fmt.Errorf("LLM router is nil")
	}
	if len(stages) == 0 {
		return nil, fmt.Errorf("chat requires at least one stage")
	}
	chat, err := c.router.NewIsolatedChatFactory(stages...).NewChat(ctx, articleID)
	if err != nil {
		return nil, err
	}
	return &Chat{chat: chat}, nil
}

// Chat — начатый диалог с моделью. Send и Continue различаются только номером сообщения,
// и перепутанный порядок — ошибка.
type Chat struct {
	chat llm.Chat
	sent bool
}

// Send отправляет первое сообщение диалога.
func (c *Chat) Send(ctx context.Context, prompt string) (string, error) {
	if c.sent {
		return "", fmt.Errorf("chat is already started, use Continue")
	}
	c.sent = true
	return c.generate(ctx, prompt)
}

// Continue продолжает уже начатый диалог; историю держит провайдер или адаптер.
func (c *Chat) Continue(ctx context.Context, prompt string) (string, error) {
	if !c.sent {
		return "", fmt.Errorf("chat is not started, use Send")
	}
	return c.generate(ctx, prompt)
}

func (c *Chat) generate(ctx context.Context, prompt string) (string, error) {
	response, err := c.chat.Generate(ctx, prompt)
	if err != nil {
		return "", err
	}
	return response.Text, nil
}

// SkipStage снимает у диалога неиспользованные слоты названной стадии; диалог без слотов
// ничего не делает.
func (c *Chat) SkipStage(stage string) {
	if skipper, ok := c.chat.(interface{ SkipStage(string) }); ok {
		skipper.SkipStage(stage)
	}
}

// Close завершает диалог и освобождает ресурсы провайдера.
func (c *Chat) Close() error { return c.chat.Close() }

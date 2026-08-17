package pprof1

import (
	"context"
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// Chat — диалог с моделью в объёме, который нужен потоку pprof_1.
//
// Интерфейс объявлен здесь, у потребителя, и держится узким намеренно: поток обязан
// выражаться через «начать диалог» и «продолжить», а не через особенности конкретного
// провайдера. DeepSeek Web — сегодня единственный адаптер этого контракта; чтобы подключить
// OpenAI API, достаточно второй реализации ChatFactory, а последовательность стадий не
// изменится.
type Chat interface {
	// Send отправляет первое сообщение диалога.
	Send(ctx context.Context, prompt string) (string, error)
	// Continue продолжает уже начатый диалог. Содержимое предыдущих сообщений повторно не
	// передаётся: его держит либо провайдер, либо адаптер.
	Continue(ctx context.Context, prompt string) (string, error)
	Close() error
}

// ChatFactory открывает новый диалог. Каждый вызов — это именно новый чат, а не продолжение
// предыдущего: в потоке pprof_1 их ровно три, и границы между ними значимы.
type ChatFactory interface {
	NewChat(ctx context.Context, articleID int64, stages ...string) (Chat, error)
}

// PromptRenderer рендерит промпт стадии из её шаблона и данных.
type PromptRenderer interface {
	Prepare(call llm.Call) (llm.PreparedCall, error)
}

// routerChats — адаптер контракта над существующим роутером.
//
// Роутер уже умеет вести диалог: StageChatFactory выдаёт чат, который идёт по списку стадий
// и держится провайдера, ответившего на первое сообщение. Изолированная фабрика вдобавок
// просит провайдера начать новую беседу на первом сообщении — иначе браузерный клиент с
// одним диалогом на статью склеил бы все три чата pprof_1 в один.
type routerChats struct{ router *llm.Router }

// NewRouterChats собирает фабрику чатов поверх роутера.
func NewRouterChats(router *llm.Router) ChatFactory { return routerChats{router: router} }

func (c routerChats) NewChat(ctx context.Context, articleID int64, stages ...string) (Chat, error) {
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
	return &routerChat{chat: chat}, nil
}

// routerChat сводит Send и Continue к одному вызову провайдера: разница между ними —
// смысловая, а не техническая, и живёт в том, какое это по счёту сообщение диалога.
type routerChat struct {
	chat llm.Chat
	sent bool
}

func (c *routerChat) Send(ctx context.Context, prompt string) (string, error) {
	if c.sent {
		return "", fmt.Errorf("chat is already started, use Continue")
	}
	c.sent = true
	return c.generate(ctx, prompt)
}

func (c *routerChat) Continue(ctx context.Context, prompt string) (string, error) {
	if !c.sent {
		return "", fmt.Errorf("chat is not started, use Send")
	}
	return c.generate(ctx, prompt)
}

func (c *routerChat) generate(ctx context.Context, prompt string) (string, error) {
	response, err := c.chat.Generate(ctx, prompt)
	if err != nil {
		return "", err
	}
	return response.Text, nil
}

func (c *routerChat) Close() error { return c.chat.Close() }

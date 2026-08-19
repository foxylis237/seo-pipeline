package pprof2

import (
	"context"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/llmchat"
)

// Chat — диалог с моделью в объёме, который нужен потоку pprof_2.
//
// Интерфейс объявлен здесь, у потребителя, и держится узким намеренно: поток обязан
// выражаться через «начать диалог» и «продолжить», а не через особенности конкретного
// провайдера. DeepSeek Web — сегодня единственный адаптер этого контракта; чтобы подключить
// другого провайдера, достаточно второй реализации ChatFactory, а последовательность стадий
// не изменится.
type Chat interface {
	// Send отправляет первое сообщение диалога.
	Send(ctx context.Context, prompt string) (string, error)
	// Continue продолжает уже начатый диалог. Содержимое предыдущих сообщений повторно не
	// передаётся: его держит либо провайдер, либо адаптер.
	Continue(ctx context.Context, prompt string) (string, error)
	Close() error
}

// ChatFactory открывает новый диалог. Каждый вызов — это именно новый чат, а не продолжение
// предыдущего: в потоке pprof_2 их ровно три, и границы между ними значимы.
type ChatFactory interface {
	NewChat(ctx context.Context, articleID int64, stages ...string) (Chat, error)
}

// PromptRenderer рендерит промпт стадии из её шаблона и данных.
type PromptRenderer interface {
	Prepare(call llm.Call) (llm.PreparedCall, error)
}

// routerChats поднимает общий адаптер диалогов до контракта этого потока.
//
// Сам адаптер живёт в internal/pipeline/llmchat и о задачах не знает. Обёртка нужна потому,
// что он возвращает свой конкретный тип, а поток объявил у себя интерфейс: подменить в
// тестах фабрику целиком иначе было бы нечем.
type routerChats struct{ chats llmchat.RouterChats }

// NewRouterChats собирает фабрику чатов поверх роутера.
func NewRouterChats(router *llm.Router) ChatFactory {
	return routerChats{chats: llmchat.NewRouterChats(router)}
}

func (c routerChats) NewChat(ctx context.Context, articleID int64, stages ...string) (Chat, error) {
	chat, err := c.chats.NewChat(ctx, articleID, stages...)
	if err != nil {
		return nil, err
	}
	return chat, nil
}

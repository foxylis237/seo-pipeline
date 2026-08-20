package taskflow

import (
	"context"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/llmchat"
)

// Chat — диалог с моделью в объёме, который нужен потоку задачи.
//
// Контракт держится узким намеренно: поток обязан выражаться через «начать диалог» и
// «продолжить», а не через особенности конкретного провайдера. DeepSeek Web — сегодня
// единственный адаптер; чтобы подключить другого провайдера, достаточно второй реализации
// ChatFactory, а последовательность стадий не изменится.
type Chat interface {
	// Send отправляет первое сообщение диалога.
	Send(ctx context.Context, prompt string) (string, error)
	// Continue продолжает уже начатый диалог. Содержимое предыдущих сообщений повторно не
	// передаётся: его держит либо провайдер, либо адаптер.
	Continue(ctx context.Context, prompt string) (string, error)
	Close() error
}

// Send — одно сообщение диалога: либо Chat.Send, либо Chat.Continue. Тип нужен затем, что
// поток выбирает между ними по месту, а дальше сообщение идёт общим путём.
type Send func(ctx context.Context, prompt string) (string, error)

// ChatFactory открывает новый диалог. Каждый вызов — именно новый чат, а не продолжение
// предыдущего: границы между чатами в потоке задачи значимы.
type ChatFactory interface {
	NewChat(ctx context.Context, articleID int64, stages ...string) (Chat, error)
}

// routerChats поднимает общий адаптер диалогов до контракта потока.
//
// Сам адаптер живёт в internal/pipeline/llmchat и о задачах не знает. Обёртка нужна потому,
// что он возвращает свой конкретный тип, а поток объявил интерфейс: подменить в тестах
// фабрику целиком иначе было бы нечем.
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

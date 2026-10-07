package taskflow

import (
	"context"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/llmchat"
)

// Chat — диалог с моделью в объёме, который нужен потоку задачи: начать и продолжить.
type Chat interface {
	// Send отправляет первое сообщение диалога.
	Send(ctx context.Context, prompt string) (string, error)
	// Continue продолжает начатый диалог; историю держит провайдер или адаптер.
	Continue(ctx context.Context, prompt string) (string, error)
	Close() error
}

// Send — одно сообщение диалога: Chat.Send или Chat.Continue.
type Send func(ctx context.Context, prompt string) (string, error)

// ChatFactory открывает новый диалог; каждый вызов — новый чат, а не продолжение предыдущего.
type ChatFactory interface {
	NewChat(ctx context.Context, articleID int64, stages ...string) (Chat, error)
}

// routerChats приводит llmchat.RouterChats, возвращающий конкретный тип, к интерфейсу ChatFactory.
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

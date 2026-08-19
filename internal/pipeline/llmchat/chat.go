// Package llmchat даёт поток сообщений одного диалога поверх роутера LLM.
//
// Пакет ничего не знает ни о задачах, ни о порядке их стадий: он отвечает на один вопрос —
// «как начать новую беседу с моделью и как её продолжить». Кто и в каком порядке шлёт
// сообщения, решает поток задачи, объявляя у себя интерфейс ровно той ширины, которая ему
// нужна: типы отсюда удовлетворяют его структурно, и задача не обязана импортировать этот
// пакет ради объявления.
package llmchat

import (
	"context"
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// RouterChats открывает диалоги поверх существующего роутера.
//
// Роутер уже умеет вести беседу: StageChatFactory выдаёт чат, который идёт по списку стадий и
// держится провайдера, ответившего на первое сообщение. Изолированная фабрика вдобавок просит
// провайдера начать новую беседу на первом сообщении — иначе браузерный клиент с одним
// диалогом на статью склеил бы все чаты задачи в один.
type RouterChats struct{ router *llm.Router }

// NewRouterChats собирает фабрику чатов поверх роутера.
func NewRouterChats(router *llm.Router) RouterChats { return RouterChats{router: router} }

// NewChat открывает новый диалог по списку стадий. Каждый вызов — именно новый чат, а не
// продолжение предыдущего: границы между чатами в потоке задачи значимы.
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

// Chat — начатый диалог с моделью.
//
// Send и Continue сводятся к одному вызову провайдера: разница между ними смысловая, а не
// техническая, и живёт в том, какое это по счёту сообщение. Ошибка вместо молчаливого
// поведения нужна затем, что перепутанный порядок означает потерянную историю — а именно на
// историю опираются вторые и третьи сообщения чата.
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

// Continue продолжает уже начатый диалог. Содержимое предыдущих сообщений повторно не
// передаётся: его держит либо провайдер, либо адаптер.
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

// Close завершает диалог и освобождает ресурсы провайдера.
func (c *Chat) Close() error { return c.chat.Close() }

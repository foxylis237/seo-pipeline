package deepseekweb

import (
	"context"
	"math/rand"
	"time"
)

// pacing разносит запросы к веб-интерфейсу во времени.
//
// Поля-функции подменяются в тестах: иначе проверка паузы в 30–50 s занимала бы столько же
// реального времени.
type pacing struct {
	minInterval time.Duration
	jitter      time.Duration
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
	random      func(int64) int64
}

func defaultPacing() pacing {
	return pacing{
		minInterval: minRequestInterval,
		jitter:      requestJitter,
		now:         time.Now,
		sleep:       sleepContext,
		random:      func(limit int64) int64 { return rand.Int63n(limit) },
	}
}

// interval возвращает целевой промежуток между запросами: минимум плюс случайная добавка.
func (p pacing) interval() time.Duration {
	if p.jitter <= 0 {
		return p.minInterval
	}
	return p.minInterval + time.Duration(p.random(int64(p.jitter)))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// WaitBeforeRequest выдерживает паузу с момента завершения прошлого запроса.
//
// Вызывается роутером ДО того, как на стадию наложен её таймаут: пауза — это осознанное
// самоограничение, а не работа модели, и вычитать её из бюджета генерации нельзя. Прогон
// статьи 47 упёрся именно в это: 58 s паузы съели треть 180-секундного бюджета стадии.
//
// Первый запрос процесса не ждёт: пауза защищает от частоты обращений, а не от самого факта
// обращения, и стартовая задержка ничего не даёт.
func (c *Client) WaitBeforeRequest(ctx context.Context) error {
	c.mu.Lock()
	last := c.lastRequestAt
	c.mu.Unlock()
	if last.IsZero() {
		return nil
	}
	interval := c.pace.interval()
	elapsed := c.pace.now().Sub(last)
	wait := interval - elapsed
	if wait <= 0 {
		return nil
	}
	c.logger.Info("DeepSeek Web request throttled",
		"wait_ms", wait.Milliseconds(),
		"interval_ms", interval.Milliseconds(),
		"since_previous_ms", elapsed.Milliseconds(),
	)
	return c.pace.sleep(ctx, wait)
}

func (c *Client) markRequestFinished() {
	c.mu.Lock()
	c.lastRequestAt = c.pace.now()
	c.mu.Unlock()
}

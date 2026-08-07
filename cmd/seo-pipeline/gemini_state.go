package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// geminiStatePath хранит момент, до которого Gemini считается недоступным.
	//
	// Файл, а не PostgreSQL: это состояние провайдера, а не статьи, и оно должно читаться
	// до подключения к базе. Формат тот же, что у маркера блокировки DeepSeek рядом с
	// профилем браузера, — строка RFC3339 и причина.
	geminiStatePath = "data/llm/gemini-unavailable"

	// geminiUnavailableTTL — на сколько Gemini выключается после исчерпания квоты.
	geminiUnavailableTTL = 24 * time.Hour
)

// geminiAvailability — состояние доступности Gemini, переживающее перезапуск приложения.
type geminiAvailability struct {
	path string
	now  func() time.Time
}

func newGeminiAvailability() geminiAvailability {
	return geminiAvailability{path: geminiStatePath, now: time.Now}
}

// state сообщает, выключен ли Gemini сейчас. Метод чистый: ничего не удаляет и не пишет.
// Побочный эффект здесь стоил бы дорого — состояние читается на каждой статье.
//
// present отличает «маркера нет» от «маркер есть, но срок истёк».
func (a geminiAvailability) state() (until time.Time, reason string, blocked, present bool) {
	raw, err := os.ReadFile(a.path)
	if err != nil {
		return time.Time{}, "", false, false
	}
	lines := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)
	until, err = time.Parse(time.RFC3339, strings.TrimSpace(lines[0]))
	if err != nil {
		// Битый маркер не должен выключать провайдера навсегда.
		return time.Time{}, "", false, false
	}
	if len(lines) > 1 {
		reason = strings.TrimSpace(lines[1])
	}
	return until, reason, a.now().Before(until), true
}

// disable выключает Gemini на TTL и возвращает момент, до которого он недоступен.
func (a geminiAvailability) disable(reason string) (time.Time, error) {
	until := a.now().Add(geminiUnavailableTTL)
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return until, fmt.Errorf("создать каталог состояния LLM: %w", err)
	}
	content := until.UTC().Format(time.RFC3339) + "\n" + reason + "\n"
	if err := os.WriteFile(a.path, []byte(content), 0o600); err != nil {
		return until, fmt.Errorf("сохранить состояние Gemini: %w", err)
	}
	return until, nil
}

// expire снимает истёкший маркер и сообщает, был ли он снят. Вызывается один раз при старте:
// решение о режиме от него не зависит — state уже сообщает, что срок вышел.
func (a geminiAvailability) expire() (until time.Time, reason string, removed bool, err error) {
	until, reason, blocked, present := a.state()
	if !present || blocked {
		return until, reason, false, nil
	}
	return until, reason, true, a.clear()
}

// clear снимает выключение: маркер истёк или доступность восстановлена вручную.
func (a geminiAvailability) clear() error {
	if err := os.Remove(a.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("удалить состояние Gemini: %w", err)
	}
	return nil
}

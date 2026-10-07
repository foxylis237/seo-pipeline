package pagebatch

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Пороги предохранителя прогона.
const (
	// DefaultSameReasonLimit — сколько подряд отказов с одной причиной останавливают прогон.
	DefaultSameReasonLimit = 2
	// DefaultFailureLimit — сколько подряд отказов с любыми причинами останавливают прогон.
	DefaultFailureLimit = 3
)

// ErrRunStopped — прогон остановлен предохранителем; оставшиеся статьи не тронуты.
var ErrRunStopped = errors.New("прогон остановлен: отказы перестали быть единичными")

// FailureGuard решает, когда прогон пора останавливать, по двум независимым счётчикам подряд идущих отказов.
// Повторы внутри статьи не считаются: «сервер занят» повторяется дословно и на третьей попытке часто проходит.
type FailureGuard struct {
	// SameReasonLimit и FailureLimit — пороги; нулевые значения означают умолчания.
	SameReasonLimit int
	FailureLimit    int

	reason     string
	sameReason int
	inARow     int
}

// NewFailureGuard собирает предохранитель с порогами по умолчанию.
func NewFailureGuard() *FailureGuard {
	return &FailureGuard{SameReasonLimit: DefaultSameReasonLimit, FailureLimit: DefaultFailureLimit}
}

// Passed отмечает пройденную статью и обнуляет счётчики.
func (g *FailureGuard) Passed() {
	g.reason, g.sameReason, g.inARow = "", 0, 0
}

// Failed регистрирует отказ статьи; непустая ошибка оборачивает ErrRunStopped.
func (g *FailureGuard) Failed(err error) error {
	sameLimit, totalLimit := g.SameReasonLimit, g.FailureLimit
	if sameLimit <= 0 {
		sameLimit = DefaultSameReasonLimit
	}
	if totalLimit <= 0 {
		totalLimit = DefaultFailureLimit
	}
	if reason := failureReason(err); reason == g.reason {
		g.sameReason++
	} else {
		g.reason, g.sameReason = reason, 1
	}
	g.inARow++
	if g.sameReason >= sameLimit {
		// Два %w: опознаётся и как остановка прогона, и как исходная причина.
		return fmt.Errorf("%w: %d статей подряд упали по одной и той же причине (%w)",
			ErrRunStopped, g.sameReason, err)
	}
	if g.inARow >= totalLimit {
		return fmt.Errorf("%w: %d статей подряд не пройдены", ErrRunStopped, g.inARow)
	}
	return nil
}

var (
	reasonQuotedRE = regexp.MustCompile(`"[^"]*"`)
	reasonNumberRE = regexp.MustCompile(`\d+`)
	reasonSpaceRE  = regexp.MustCompile(`\s+`)
)

// failureReason сводит ошибку к ключу группировки без кавычек и чисел (стадия, ID, длительности).
// Текст, а не тип: весь прогон падает одним типом с разным текстом внутри.
func failureReason(err error) string {
	text := strings.ToLower(err.Error())
	text = reasonQuotedRE.ReplaceAllString(text, "")
	text = reasonNumberRE.ReplaceAllString(text, "")
	return strings.TrimSpace(reasonSpaceRE.ReplaceAllString(text, " "))
}

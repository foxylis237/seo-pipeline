package deepseekweb

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

func TestCaptchaErrorIsNotRetryableAndReadable(t *testing.T) {
	err := captchaError()
	var statusErr *llm.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %#v", err)
	}
	if statusErr.Type != llm.ErrorTypeUnauthorized {
		t.Fatalf("type = %s: против капчи не должно быть автоматических повторов", statusErr.Type)
	}
	if err.Error() != "DeepSeek requires manual captcha verification" {
		t.Fatalf("message = %q", err.Error())
	}
}

// Блокировка аккаунта и капча лечатся по-разному: первая выключает провайдера на cooldown,
// вторая — разовая помеха, после которой профиль и авторизация остаются нетронутыми.
func TestBlockingReasonsWriteCooldownButChallengeDoesNot(t *testing.T) {
	for _, reason := range []string{"account_blocked", "terms_violation", "access_restricted"} {
		profileDir := t.TempDir()
		clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
		client := testClient(t, profileDir, &clock, 0, nil)
		if err := client.blockAccount(reason); !strings.Contains(err.Error(), "deepseek_account_unavailable") {
			t.Fatalf("%s: error = %v", reason, err)
		}
		if _, _, blocked := readBlockedUntil(profileDir, clock); !blocked {
			t.Fatalf("%s: cooldown не записан", reason)
		}
	}
}

func TestChallengeReasonIsRoutedAwayFromBlocking(t *testing.T) {
	if reasonChallenge != "challenge_or_captcha" {
		t.Fatalf("reasonChallenge = %q, скрипт возвращает challenge_or_captcha", reasonChallenge)
	}
	if !strings.Contains(blockedStateJS, `"`+reasonChallenge+`"`) {
		t.Fatalf("скрипт не возвращает причину %q", reasonChallenge)
	}
}

// Профиль после капчи должен остаться на месте: удаление снесло бы авторизацию, которую
// пользователь только что подтвердил руками.
func TestChallengeHandlingKeepsProfileAndSkipsCooldown(t *testing.T) {
	profileDir := t.TempDir()
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	client := testClient(t, profileDir, &clock, 0, nil)

	source := readSourceFile(t, "challenge.go")
	for _, forbidden := range []string{"RemoveAll", "writeBlockedUntil", "blockCooldown"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("обработка капчи использует %q: профиль или cooldown не должны затрагиваться", forbidden)
		}
	}
	if _, _, blocked := readBlockedUntil(profileDir, clock); blocked {
		t.Fatal("cooldown появился без блокировки аккаунта")
	}
	if client.cfg.ProfileDir != profileDir {
		t.Fatal("каталог профиля подменён")
	}
}

func TestCaptchaWaitIsBoundedAndPolls(t *testing.T) {
	if captchaWaitTimeout < time.Minute || captchaWaitTimeout > 15*time.Minute {
		t.Fatalf("captchaWaitTimeout = %v: человеку нужно время, но ожидание должно быть ограничено", captchaWaitTimeout)
	}
	if captchaPollInterval >= captchaWaitTimeout {
		t.Fatalf("интервал опроса %v не меньше ожидания %v", captchaPollInterval, captchaWaitTimeout)
	}
}

// Видимое окно обязано открываться на том же профиле, иначе пройденная проверка потеряется.
func TestManualVerificationOpensSameProfileHeadful(t *testing.T) {
	source := readSourceFile(t, "challenge.go")
	if !strings.Contains(source, "launchBrowser(c.cfg.ProfileDir, false)") {
		t.Fatal("видимое окно открывается не на текущем профиле или не в headful-режиме")
	}
	if !strings.Contains(source, "c.resetSession()") {
		t.Fatal("headless-сессия не закрывается: flock не отдаст профиль видимому окну")
	}
}

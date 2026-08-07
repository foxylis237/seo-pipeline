package deepseekweb

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// filledProfile имитирует сохранённый профиль Chromium: вложенные каталоги и файл состояния.
func filledProfile(t *testing.T) string {
	t.Helper()
	profileDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(profileDir, "Default", "Local Storage"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Default/Cookies", "Default/Local Storage/state", profileLockName} {
		if err := os.WriteFile(filepath.Join(profileDir, filepath.FromSlash(name)), []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return profileDir
}

func TestResetProfileRemovesSavedState(t *testing.T) {
	profileDir := filledProfile(t)
	if err := resetProfile(profileDir, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
		t.Fatalf("профиль остался на диске: %v", err)
	}
}

func TestResetProfileIsNoopWithoutProfile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "нет-такого-профиля")
	if err := resetProfile(missing, discardLogger()); err != nil {
		t.Fatalf("отсутствующий профиль вернул ошибку: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("resetProfile создал каталог, хотя удалять было нечего")
	}
}

func TestResetProfileRefusesWhenProfileIsBusy(t *testing.T) {
	profileDir := filledProfile(t)
	lock, err := os.OpenFile(profileLockPath(profileDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	err = resetProfile(profileDir, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "already used by another process") {
		t.Fatalf("error = %v, want отказ по занятому профилю", err)
	}
	if _, statErr := os.Stat(filepath.Join(profileDir, "Default", "Cookies")); statErr != nil {
		t.Fatalf("занятый профиль был повреждён: %v", statErr)
	}
}

func TestResetProfileReleasesLockForBrowser(t *testing.T) {
	profileDir := filledProfile(t)
	if err := resetProfile(profileDir, discardLogger()); err != nil {
		t.Fatal(err)
	}
	// launchBrowser берёт ту же блокировку: после resetProfile она должна быть свободна.
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensureProfileIsFree(profileDir); err != nil {
		t.Fatalf("блокировка осталась захваченной: %v", err)
	}
}

func TestResetProfileClearsBlockCooldown(t *testing.T) {
	profileDir := filledProfile(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if err := writeBlockedUntil(profileDir, time.Now().Add(time.Hour), "account_blocked"); err != nil {
		t.Fatal(err)
	}
	if _, _, blocked := readBlockedUntil(profileDir, now); !blocked {
		t.Fatal("подготовка теста: cooldown не записан")
	}
	if err := resetProfile(profileDir, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if _, _, blocked := readBlockedUntil(profileDir, now); blocked {
		t.Fatal("cooldown пережил очистку профиля")
	}
}

func TestResetProfileLogsClearedCooldown(t *testing.T) {
	profileDir := filledProfile(t)
	if err := writeBlockedUntil(profileDir, time.Now().Add(time.Hour), "account_blocked"); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if err := resetProfile(profileDir, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "block cooldown was cleared") {
		t.Fatalf("сброс cooldown не попал в лог: %s", logs.String())
	}
}

func TestResetProfileDoesNotLogCooldownWhenAbsent(t *testing.T) {
	profileDir := filledProfile(t)
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if err := resetProfile(profileDir, logger); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "block cooldown was cleared") {
		t.Fatalf("сообщение о сбросе cooldown без самого cooldown: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "profile removed before manual login") {
		t.Fatalf("удаление профиля не залогировано: %s", logs.String())
	}
}

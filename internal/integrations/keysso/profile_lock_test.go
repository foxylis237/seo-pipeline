package keysso

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

func TestProfileLockRejectsSecondService(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(profilePath, 0o700); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := New(Config{}, logger)
	if err := first.lockProfile(); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	second := New(Config{}, logger)
	if err := second.lockProfile(); err == nil {
		_ = second.Close()
		t.Fatal("second service locked a profile that is already in use")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	if err := second.lockProfile(); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}
}

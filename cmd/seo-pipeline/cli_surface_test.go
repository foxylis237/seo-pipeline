package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
)

// Snapshots are rewritten with UPDATE_SNAPSHOTS=1 go test ./cmd/seo-pipeline -run Snapshot.
const updateSnapshotsEnv = "UPDATE_SNAPSHOTS"

var cliSurfaceOperations = []string{
	"import", "import-check", "errors", "keywords", "retry", "run", "regenerate", "dry-run",
	"prepare", "generate", "demo-generate", "article", "info", "review", "fix", "html", "result",
	"report", "clear", "reset", "google-login", "google-publish", "deepseek-login",
	"wordpress-check", "publish", "republish", "mark-published", "catalog-sync", "catalog-show",
}

func TestCLISurfaceSnapshot(t *testing.T) {
	var cases [][]string
	add := func(args ...string) { cases = append(cases, append([]string{"seo-pipeline"}, args...)) }

	add()
	add("unknown")
	add("article", "37")
	add("--dry-run")
	for _, name := range []string{"login", "LOGIN"} {
		add(name)
		for _, service := range []string{"deepseek", "google", "keysso", "arsenkin", "unknown"} {
			add(name, service)
		}
		add(name, "deepseek", "extra")
	}
	for _, profile := range taskRegistry() {
		for _, task := range []string{profile.Command, profile.Name} {
			add(task)
			add(task, "unknown")
		}
		task := profile.Command
		for _, operation := range cliSurfaceOperations {
			add(task, operation)
			add(task, operation, "37")
			add(task, operation, " 37 ")
			add(task, operation, "0")
			add(task, operation, "wrong")
			add(task, operation, "37", "38")
			add(task, operation, "37", "38", "39")
			add(task, operation, "--plan")
			add(task, operation, "--yes")
			add(task, operation, "--dry-run")
		}
		add(task, "run", "--plan", "37")
		add(task, "publish", "--plan", "37")
		add(task, "run", "--plan", "--dry-run")
		add(task, "run", "--dry-run", "--dry-run")
		add("--dry-run", task, "run")
		add(task, "reset", "--yes", "37")
		add(task, "mark-published", "16", "0")
		add(task, "mark-published", "16", "wrong")
		add(task, "import", "-1")
	}

	var out strings.Builder
	for _, args := range cases {
		fmt.Fprintf(&out, "%q\n  %s\n", args[1:], describeParsedCommand(args))
	}
	compareSnapshot(t, "cli_surface.golden", out.String())
}

func describeParsedCommand(args []string) string {
	command, err := parseCommand(args)
	if err != nil {
		return "error: " + err.Error()
	}
	login, _ := loginService(command)
	validation := "ok"
	if command.Name != loginCommandName {
		if err := validateConfig(command.Name, config.Config{DatabaseURL: "postgres://snapshot"}); err != nil {
			validation = err.Error()
		}
	}
	return fmt.Sprintf("task=%s name=%s external_id=%s import_limit=%d dry_run=%t plan=%t yes=%t post_id=%d service=%s login=%s validate=%s",
		command.Profile.Command, command.Name, command.ExternalID, command.ImportLimit, command.DryRun,
		command.Plan, command.AssumeYes, command.WordPressPostID, command.Service, login, validation)
}

func compareSnapshot(t *testing.T, name, got string) {
	t.Helper()
	compareSnapshotIn(t, "testdata", name, got)
}

func compareSnapshotIn(t *testing.T, dir, name, got string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if os.Getenv(updateSnapshotsEnv) == "1" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot %s (create it with %s=1): %v", path, updateSnapshotsEnv, err)
	}
	if want := string(wantBytes); want != got {
		t.Fatalf("snapshot %s differs (update with %s=1 if intended):\n%s", path, updateSnapshotsEnv, lineDiff(want, got))
	}
}

func lineDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	var diff strings.Builder
	shown := 0
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w == g {
			continue
		}
		fmt.Fprintf(&diff, "line %d:\n- %s\n+ %s\n", i+1, w, g)
		if shown++; shown == 20 {
			diff.WriteString("…\n")
			break
		}
	}
	return diff.String()
}

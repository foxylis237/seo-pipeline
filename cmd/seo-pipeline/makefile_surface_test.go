package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Listed here rather than read from the Makefile, so a target dropped there shows up in the diff.
var makefileSurfaceTasks = []string{
	"task-1", "pprof-1", "pprof-2", "obuch-1", "obuch-2", "pprof-audit-1", "pprof-audit-2",
}

func TestMakefileSurfaceSnapshot(t *testing.T) {
	cases := [][]string{
		{"help"},
		{"login"},
		{"login", "deepseek"},
		{"login", "deepseek", "extra"},
		{"unknown-target"},
		{"build"},
		{"test"},
		{"lint"},
	}
	for _, task := range makefileSurfaceTasks {
		cases = append(cases,
			[]string{task},
			[]string{task, "unknown"},
			[]string{task, "import", "0"},
			[]string{task, "prepare", "wrong"},
			[]string{task, "run", "37", "38"},
			[]string{task, "run", "plan"},
			[]string{task, "run", "plan", "5"},
			[]string{task, "publish", "plan"},
			[]string{task, "publish", "plan", "5"},
			[]string{task, "mark-published", "16", "21593"},
			[]string{task, "mark-published", "16", "x"},
			[]string{task, "dry-run", "1"},
			[]string{task, "deepseek-login", "1"},
			[]string{task, "google-login", "1"},
		)
		for _, operation := range cliSurfaceOperations {
			cases = append(cases, []string{task, operation}, []string{task, operation, "37"})
		}
	}

	results := make([]string, len(cases))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 8)
	for i, args := range cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			results[i] = runMakeSurface(t, args)
		}()
	}
	wg.Wait()

	var out strings.Builder
	for i, args := range cases {
		fmt.Fprintf(&out, "make %s\n%s\n", strings.Join(args, " "), results[i])
	}
	compareSnapshot(t, "makefile_surface.golden", out.String())
}

func runMakeSurface(t *testing.T, args []string) string {
	t.Helper()
	command := exec.Command("make", append([]string{"-s", "GO=echo", "DOCKER_COMPOSE=echo", "GOLANGCI_LINT=echo"}, args...)...)
	command.Dir = filepath.Join("..", "..")
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "MAKEFLAGS=") && !strings.HasPrefix(env, "MFLAGS=") && !strings.HasPrefix(env, "MAKELEVEL=") {
			command.Env = append(command.Env, env)
		}
	}
	output, err := command.CombinedOutput()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Errorf("make %v: %v", args, err)
			return ""
		}
		exit = exitErr.ExitCode()
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		// GNU make formats its own messages differently across versions; the exit code is kept.
		if strings.HasPrefix(line, "make: ") {
			continue
		}
		out.WriteString("  | " + line + "\n")
	}
	fmt.Fprintf(&out, "  exit %d", exit)
	return out.String()
}

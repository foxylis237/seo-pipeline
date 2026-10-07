package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Live snapshots run the built binary against the owner's database and input books;
// make snapshot-save records them outside git, make snapshot-check compares.
const (
	snapshotDirEnv     = "SNAPSHOT_DIR"
	snapshotBinEnv     = "SNAPSHOT_BIN"
	snapshotWorkdirEnv = "SNAPSHOT_WORKDIR"
)

type liveSnapshot struct {
	dir, bin, workdir string
}

func liveSnapshotEnv(t *testing.T) liveSnapshot {
	t.Helper()
	env := liveSnapshot{dir: os.Getenv(snapshotDirEnv), bin: os.Getenv(snapshotBinEnv), workdir: os.Getenv(snapshotWorkdirEnv)}
	if env.dir == "" {
		t.Skipf("%s is not set; run make snapshot-check", snapshotDirEnv)
	}
	if env.bin == "" || env.workdir == "" {
		t.Fatalf("%s and %s are required with %s", snapshotBinEnv, snapshotWorkdirEnv, snapshotDirEnv)
	}
	return env
}

var logErrorRE = regexp.MustCompile(`error="(?:[^"\\]|\\.)*"`)

// run returns the command's output without log lines; a failed command keeps its error text.
func (env liveSnapshot) run(t *testing.T, extraEnv []string, args ...string) string {
	t.Helper()
	command := exec.Command(env.bin, args...)
	command.Dir = env.workdir
	command.Env = append(os.Environ(), extraEnv...)
	output, err := command.CombinedOutput()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("%s %v: %v", env.bin, args, err)
		}
		exit = exitErr.ExitCode()
	}
	var out strings.Builder
	fmt.Fprintf(&out, "$ %s\n", strings.Join(args, " "))
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		if !strings.HasPrefix(line, "time=") {
			out.WriteString(line + "\n")
			continue
		}
		if strings.Contains(line, " level=ERROR ") {
			out.WriteString("ERROR " + logErrorRE.FindString(line) + "\n")
		}
	}
	fmt.Fprintf(&out, "exit %d\n\n", exit)
	return out.String()
}

var firstExternalIDRE = regexp.MustCompile(`external_id=(\d+)`)

func TestLiveDatabaseCommandsSnapshot(t *testing.T) {
	env := liveSnapshotEnv(t)
	for _, profile := range taskRegistry() {
		t.Run(profile.Command, func(t *testing.T) {
			var out strings.Builder
			if profile.ArticleAudit != nil {
				out.WriteString(env.run(t, nil, profile.Command, "report"))
			} else {
				out.WriteString(env.run(t, nil, profile.Command, "errors"))
				out.WriteString(env.run(t, nil, profile.Command, "run", "--plan"))
				importCheck := env.run(t, nil, profile.Command, "import-check")
				out.WriteString(importCheck)
				if match := firstExternalIDRE.FindStringSubmatch(importCheck); match != nil {
					out.WriteString(env.run(t, nil, profile.Command, "catalog-show", match[1]))
				}
			}
			compareSnapshotIn(t, env.dir, "db-"+profile.Command+".txt", out.String())
		})
	}
}

// The output directory is pinned to the profile default, so a local OUTPUT_DIR cannot move it.
func TestLiveDryRunSnapshot(t *testing.T) {
	env := liveSnapshotEnv(t)
	for _, profile := range taskRegistry() {
		if profile.ArticleAudit != nil {
			continue
		}
		t.Run(profile.Command, func(t *testing.T) {
			var out strings.Builder
			out.WriteString(env.run(t, []string{"APP_ENV=test", profile.EnvPrefix + "OUTPUT_DIR=" + profile.OutputDir},
				profile.Command, "run", "--dry-run"))
			root := filepath.Join(env.workdir, profile.OutputDir, "dry-run")
			err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				fmt.Fprintf(&out, "=== %s\n%s\n", filepath.ToSlash(relative), content)
				return nil
			})
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("read dry-run output: %v", err)
			}
			compareSnapshotIn(t, env.dir, "dry-run-"+profile.Command+".txt", out.String())
		})
	}
}

// Package archtest checks the direction of dependencies between packages.
package archtest

import (
	"errors"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// forbidden is a dependency kind a business package must not import.
type forbidden struct {
	name    string
	matches func(module, imp string) bool
}

func hasPathPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

var (
	pgx = forbidden{"pgx", func(_, imp string) bool {
		return hasPathPrefix(imp, "github.com/jackc/pgx")
	}}
	playwright = forbidden{"playwright", func(_, imp string) bool {
		return hasPathPrefix(imp, "github.com/mxschmitt/playwright-go")
	}}
	integrations = forbidden{"integrations", func(module, imp string) bool {
		return hasPathPrefix(imp, module+"/internal/integrations")
	}}
	config = forbidden{"config", func(module, imp string) bool {
		return hasPathPrefix(imp, module+"/internal/config")
	}}
)

// rule applies forbid to every package under one of roots, except the adapters in skip.
type rule struct {
	roots  []string
	skip   []string
	forbid []forbidden
}

var rules = []rule{
	{
		roots:  []string{"internal/pipeline", "internal/tasks", "internal/catalog"},
		skip:   []string{"internal/pipeline/repository"},
		forbid: []forbidden{pgx, playwright, integrations, config},
	},
	{
		roots:  []string{"internal/llm"},
		forbid: []forbidden{config},
	},
}

// exceptions are the known violations; the step in the comment removes its line.
var exceptions = map[string]bool{
	"internal/catalog -> pgx":               true, // А4
	"internal/pipeline/articleaudit -> pgx": true, // А3
	"internal/llm -> config":                true, // А1
}

func (r rule) covers(pkg string) bool {
	for _, s := range r.skip {
		if hasPathPrefix(pkg, s) {
			return false
		}
	}
	for _, root := range r.roots {
		if hasPathPrefix(pkg, root) {
			return true
		}
	}
	return false
}

func TestDependencyBoundaries(t *testing.T) {
	module := goList(t, "-m")
	listing := goList(t, "-f", `{{.ImportPath}} {{join .Imports " "}}`, "./internal/...")

	found := map[string]bool{}
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkg := strings.TrimPrefix(fields[0], module+"/")
		for _, r := range rules {
			if !r.covers(pkg) {
				continue
			}
			for _, imp := range fields[1:] {
				for _, f := range r.forbid {
					if f.matches(module, imp) {
						found[pkg+" -> "+f.name] = true
					}
				}
			}
		}
	}

	var added, stale []string
	for v := range found {
		if !exceptions[v] {
			added = append(added, v)
		}
	}
	for v := range exceptions {
		if !found[v] {
			stale = append(stale, v)
		}
	}
	sort.Strings(added)
	sort.Strings(stale)
	for _, v := range added {
		t.Errorf("new dependency violation: %s", v)
	}
	for _, v := range stale {
		t.Errorf("exception no longer violated, remove it: %s", v)
	}
}

func goList(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("go list %v: %v\n%s", args, err, ee.Stderr)
		}
		t.Fatalf("go list %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

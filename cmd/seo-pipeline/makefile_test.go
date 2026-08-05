package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeImportAcceptsPositionalLimit(t *testing.T) {
	root := filepath.Join("..", "..")
	command := exec.Command("make", "-n", "task-1", "import", "10")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make task-1 import 10: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `task-1 import "10"`) {
		t.Fatalf("позиционный лимит не передан в CLI:\n%s", output)
	}
}

func TestMakeImportRejectsZeroBeforeCLI(t *testing.T) {
	root := filepath.Join("..", "..")
	command := exec.Command("make", "GO=true", "task-1", "import", "0")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Import limit must be a positive integer") {
		t.Fatalf("make task-1 import 0 должен вернуть понятную ошибку: %v\n%s", err, output)
	}
}

func TestMakeTaskOperationsAcceptOptionalID(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, operation := range []string{"prepare", "generate", "demo-generate", "article", "info", "review", "fix", "html", "result"} {
		for _, args := range [][]string{{"-n", "task-1", operation}, {"-n", "task-1", operation, "37"}} {
			command := exec.Command("make", args...)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("make %v: %v\n%s", args, err, output)
			}
			if !strings.Contains(string(output), "task-1 "+operation) {
				t.Fatalf("operation %s was not passed to CLI:\n%s", operation, output)
			}
		}
	}
}

func TestMakeTaskOperationRejectsInvalidID(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, value := range []string{"0", "-1", "wrong"} {
		args := []string{"GO=true", "task-1", "prepare", value}
		if strings.HasPrefix(value, "-") {
			args = append([]string{"--"}, args...)
		}
		command := exec.Command("make", args...)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "ID must be a positive integer") {
			t.Fatalf("make task-1 prepare %s: %v\n%s", value, err, output)
		}
	}
}

func TestMakeErrorsAcceptsOptionalExternalID(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"-n", "task-1", "errors"}, want: "task-1 errors"},
		{args: []string{"-n", "task-1", "errors", "124"}, want: `task-1 errors "124"`},
	} {
		command := exec.Command("make", test.args...)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil || !strings.Contains(string(output), test.want) {
			t.Fatalf("make %v: %v, want %q\n%s", test.args, err, test.want, output)
		}
	}
}

func TestMakeRetryAcceptsOptionalExternalID(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, args := range [][]string{{"-n", "task-1", "retry"}, {"-n", "task-1", "retry", "57"}} {
		command := exec.Command("make", args...)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "task-1 retry") {
			t.Fatalf("make %v: %v\n%s", args, err, output)
		}
	}
}

func TestOtherMakeTargetsStillResolve(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, target := range []string{"help", "test", "fmt", "vet", "build", "docker-ps"} {
		command := exec.Command("make", "-n", target)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("make -n %s: %v\n%s", target, err, output)
		}
	}
}

func TestMakeDeepSeekLoginAcceptsNoArguments(t *testing.T) {
	root := filepath.Join("..", "..")
	command := exec.Command("make", "-n", "task-1", "deepseek-login")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "task-1 deepseek-login") {
		t.Fatalf("make task-1 deepseek-login: %v\n%s", err, output)
	}
}

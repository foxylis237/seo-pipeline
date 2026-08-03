package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeImportAcceptsPositionalLimit(t *testing.T) {
	root := filepath.Join("..", "..")
	command := exec.Command("make", "-n", "import", "10")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make import 10: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `task-1 import "10"`) {
		t.Fatalf("позиционный лимит не передан в CLI:\n%s", output)
	}
}

func TestMakeImportRejectsZeroBeforeCLI(t *testing.T) {
	root := filepath.Join("..", "..")
	command := exec.Command("make", "GO=true", "import", "0")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Import limit must be a positive integer") {
		t.Fatalf("make import 0 должен вернуть понятную ошибку: %v\n%s", err, output)
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

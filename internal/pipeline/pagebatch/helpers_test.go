package pagebatch

import (
	"os"
	"strings"
)

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

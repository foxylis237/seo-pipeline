package deepseekweb

import (
	"os"
	"testing"
)

// readSourceFile нужен нескольким проверкам: поведение вокруг браузера не воспроизвести в
// юнит-тесте, но можно закрепить инварианты самого кода.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

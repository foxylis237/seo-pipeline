package arsenkin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// urlMatcherAPIs — методы playwright-go, у которых первый аргумент проходит через
// newURLMatcher. Матчер разбирает его в runtime и паникует на всём, кроме string,
// *regexp.Regexp и func(string) bool: компилятор такую ошибку не ловит, потому что
// параметр объявлен как any.
var urlMatcherAPIs = map[string]struct{}{
	"ExpectRequest":   {},
	"ExpectResponse":  {},
	"ExpectWebSocket": {},
	"WaitForURL":      {},
	"Route":           {},
	"UnRoute":         {},
	"RouteFromHAR":    {},
}

// Компилятор обязан подтверждать сигнатуру предиката, который уходит в WaitForURL
// в ensureAuthenticated: если она разъедется, сборка упадёт здесь, а не прогон в проде.
var _ func(string) bool = func(url string) bool { return !isLoginURL(url) }

// TestURLPredicatesMatchPlaywrightSignature закрывает регресс: в submitWordstat в
// ExpectRequest ушёл func(playwright.Request) bool, и прогон падал паникой в
// newURLMatcher ещё до button.Click() — POST не отправлялся, задача не создавалась,
// диагностика не собиралась. Тип аргумента — any, поэтому поймать это может только
// такая проверка.
//
// Проверяются все браузерные интеграции: ошибка не специфична для Arsenkin.
func TestURLPredicatesMatchPlaywrightSignature(t *testing.T) {
	const integrationsRoot = ".."

	checked := 0
	err := filepath.WalkDir(integrationsRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, guarded := urlMatcherAPIs[selector.Sel.Name]; !guarded {
				return true
			}
			checked++
			// Проверяем только литералы функций: именно они принимают неверную форму.
			// Строки, regexp и переменные матчер разберёт сам.
			literal, ok := call.Args[0].(*ast.FuncLit)
			if !ok {
				return true
			}
			if reason := checkURLPredicate(literal); reason != "" {
				t.Errorf("%s: %s передан предикат %s — newURLMatcher примет только func(string) bool и упадёт паникой",
					path, selector.Sel.Name, reason)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("обойти браузерные интеграции: %v", err)
	}
	if checked == 0 {
		t.Fatal("ни одного вызова playwright с URL-матчером не найдено: проверка потеряла предмет")
	}
}

// checkURLPredicate returns why the literal is not a func(string) bool, or "" when it is.
func checkURLPredicate(literal *ast.FuncLit) string {
	params := literal.Type.Params
	if count := fieldCount(params); count != 1 {
		return "с " + strconv.Itoa(count) + " параметрами вместо одного"
	}
	if name := identName(params.List[0].Type); name != "string" {
		return "с параметром " + name + " вместо string"
	}
	if count := fieldCount(literal.Type.Results); count != 1 {
		return "с " + strconv.Itoa(count) + " результатами вместо одного"
	}
	if name := identName(literal.Type.Results.List[0].Type); name != "bool" {
		return "с результатом " + name + " вместо bool"
	}
	return ""
}

func fieldCount(fields *ast.FieldList) int {
	if fields == nil {
		return 0
	}
	count := 0
	for _, field := range fields.List {
		if len(field.Names) == 0 {
			count++
			continue
		}
		count += len(field.Names)
	}
	return count
}

func identName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return identName(typed.X) + "." + typed.Sel.Name
	case *ast.StarExpr:
		return "*" + identName(typed.X)
	default:
		return "неизвестного типа"
	}
}

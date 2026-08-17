package tasks

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// maxSchemaNameLength — предел идентификатора PostgreSQL. Более длинное имя сервер молча
// обрежет, и приложение работало бы не с той схемой, которую назвали.
const maxSchemaNameLength = 63

// schemaNameRE намеренно уже, чем допускает PostgreSQL: только строчные буквы, цифры и
// подчёркивание. Имя схемы попадает в строку подключения, поэтому оно не должно требовать
// ни кавычек, ни экранирования — тогда и подставлять его в DSN безопасно.
var schemaNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// validateSchemaName проверяет имя схемы до того, как оно попадёт в DSN.
func validateSchemaName(schema string) error {
	if schema == "" {
		return fmt.Errorf("task database schema is empty")
	}
	if len(schema) > maxSchemaNameLength {
		return fmt.Errorf("task database schema %q is longer than %d characters", schema, maxSchemaNameLength)
	}
	if !schemaNameRE.MatchString(schema) {
		return fmt.Errorf("task database schema %q must match %s", schema, schemaNameRE.String())
	}
	return nil
}

// DatabaseURL направляет базовое подключение в схему задачи.
//
// Схема public возвращает DSN нетронутым: так работал task_1 до появления второй задачи, и
// любая нормализация строки здесь означала бы изменение его поведения без причины.
//
// Имя схемы проверяется до подстановки и никогда не берётся из ввода пользователя напрямую:
// профиль — единственный его источник, а validateSchemaName сужает алфавит до безопасного.
func (p Profile) DatabaseURL(baseURL string) (string, error) {
	if err := validateSchemaName(p.DBSchema); err != nil {
		return "", err
	}
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return "", fmt.Errorf("database URL is empty")
	}
	if p.DBSchema == "public" {
		return baseURL, nil
	}
	// URL-форма — единственная, которую использует проект; keyword/value поддержан потому,
	// что pgx принимает обе, и молча уронить вторую в схему public нельзя.
	if !strings.HasPrefix(base, "postgres://") && !strings.HasPrefix(base, "postgresql://") {
		return base + " search_path=" + p.DBSchema, nil
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse database URL for schema %q: %w", p.DBSchema, err)
	}
	query := parsed.Query()
	query.Set("search_path", p.DBSchema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

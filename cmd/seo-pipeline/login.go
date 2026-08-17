package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/google"
	"github.com/foxylis237/seo-pipeline/internal/llm/deepseekweb"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// loginCommandName — глобальная команда входа. Она живёт вне пространства задач: профиль
// браузера один на проект, и вход в DeepSeek или Google не принадлежит ни task_1, ни pprof_1.
const loginCommandName = "login"

// providerRegistryPath — откуда вход берёт описание провайдера.
//
// Провайдеры описаны одинаково в конфигурации каждой задачи, потому что persistent-профиль у
// них общий; входу нужны только адреса и каталог профиля, а не стадии. Поэтому берётся базовый
// файл, а не файл конкретной задачи.
const providerRegistryPath = "config/config.yaml"

// loginHandler выполняет вход в один сервис.
type loginHandler func(ctx context.Context, logger *slog.Logger) error

// loginServices — реестр сервисов с ручным входом.
//
// Keys.so и Arsenkin сюда не входят намеренно: они логинятся автоматически по KEYS_SO_* и
// ARSENKIN_* из .env, и отдельного шага человека им не требуется. Когда он понадобится,
// сервис добавляется одной записью здесь.
func loginServices() map[string]loginHandler {
	return map[string]loginHandler{
		"deepseek": runDeepSeekLogin,
		"google":   runGoogleLogin,
	}
}

// automaticLoginServices — сервисы, у которых ручного входа нет. Список нужен, чтобы
// `login keysso` отвечал объяснением, а не «unknown service».
var automaticLoginServices = map[string]string{
	"keysso":   "KEYS_SO_EMAIL и KEYS_SO_PASSWORD",
	"arsenkin": "ARSENKIN_EMAIL и ARSENKIN_PASSWORD",
}

// loginServiceNames перечисляет все известные имена сервисов для сообщений об ошибках.
func loginServiceNames() []string {
	names := make([]string, 0, len(loginServices())+len(automaticLoginServices))
	for name := range loginServices() {
		names = append(names, name)
	}
	for name := range automaticLoginServices {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseLoginCommand разбирает `seo-pipeline login <service>`.
func parseLoginCommand(args []string) (taskCommand, error) {
	if len(args) != 3 {
		return taskCommand{}, fmt.Errorf("usage: seo-pipeline %s <%s>",
			loginCommandName, strings.Join(loginServiceNames(), "|"))
	}
	service := strings.ToLower(strings.TrimSpace(args[2]))
	if _, found := loginServices()[service]; found {
		return taskCommand{Name: loginCommandName, Service: service}, nil
	}
	if credentials, found := automaticLoginServices[service]; found {
		return taskCommand{}, fmt.Errorf(
			"%s входит автоматически по %s из .env — отдельная команда входа ему не нужна", service, credentials)
	}
	return taskCommand{}, fmt.Errorf("unknown login service %q; available services: %s",
		service, strings.Join(loginServiceNames(), ", "))
}

// loginService распознаёт как глобальную команду входа, так и задачные алиасы, оставленные
// для совместимости.
func loginService(command taskCommand) (string, bool) {
	switch command.Name {
	case loginCommandName:
		return command.Service, command.Service != ""
	case "deepseek-login":
		return "deepseek", true
	case "google-login":
		return "google", true
	default:
		return "", false
	}
}

// runLogin выполняет вход в выбранный сервис.
func runLogin(ctx context.Context, service string, logger *slog.Logger) error {
	handler, found := loginServices()[service]
	if !found {
		return fmt.Errorf("unknown login service %q", service)
	}
	serviceLogger := logger.With("operation", loginCommandName, "service", service)
	if err := handler(ctx, serviceLogger); err != nil {
		return err
	}
	serviceLogger.Info("вход выполнен, persistent-профиль сохранён")
	return nil
}

func runDeepSeekLogin(ctx context.Context, logger *slog.Logger) error {
	provider, err := config.LoadLLMProviderConfig(providerRegistryPath, "deepseek_web")
	if err != nil {
		return err
	}
	if provider.Type != "deepseek_web" {
		return fmt.Errorf("DeepSeek Web provider is not configured as llm.providers.deepseek_web")
	}
	return deepseekweb.Login(ctx, deepseekweb.Config{
		ChatURL: provider.ChatURL, LoginURL: provider.LoginURL, ProfileDir: provider.ProfileDir,
	}, logger.With("provider", "deepseek_web"))
}

func runGoogleLogin(ctx context.Context, logger *slog.Logger) error {
	// Диагностика входа не принадлежит задаче — вход общий, поэтому каталог по умолчанию
	// интеграции здесь и нужен.
	return google.Login(ctx, googleConfig(false, ""), logger)
}

// diagnosticsDirs — корни диагностики интеграций одной задачи.
//
// Собираются в composition root один раз. Дальше по коду ходят уже готовые строки: ни движок,
// ни интеграции не должны получать профиль задачи целиком.
type diagnosticsDirs struct {
	keysso   string
	arsenkin string
	deepseek string
	google   string
}

func newDiagnosticsDirs(profile tasks.Profile) diagnosticsDirs {
	return diagnosticsDirs{
		keysso:   profile.DiagnosticsSubdir("keysso"),
		arsenkin: profile.DiagnosticsSubdir("arsenkin"),
		deepseek: profile.DiagnosticsSubdir("deepseek"),
		google:   profile.DiagnosticsSubdir("google"),
	}
}

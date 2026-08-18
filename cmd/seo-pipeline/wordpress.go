package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
)

// wordPressCheckOperation — проверка подключения к площадке задачи.
//
// Операция задачная, а не глобальная: площадка у каждой задачи своя, и вопрос «а какой сайт
// проверяем?» не должен иметь второго ответа. Имя по конвенции google-publish и google-login —
// сервис, затем действие.
const wordPressCheckOperation = "wordpress-check"

const (
	// wordPressRequestTimeout — бюджет одной попытки.
	wordPressRequestTimeout = 10 * time.Second
	// wordPressCheckDeadline — потолок всей проверки вместе с повторами и паузами.
	//
	// Стоит явно, а не выводится перемножением попыток на таймаут: вложенные таймауты
	// умножаются молча, и однажды это уже стоило проекту десятиминутной стадии (ловушка H6).
	wordPressCheckDeadline = 35 * time.Second
)

// wordPressChecker — то, что команда требует от клиента.
//
// Интерфейс объявлен здесь, у потребителя: благодаря этому вывод и разбор отказов проверяются
// без живого сайта и без TLS-сервера в тестах.
type wordPressChecker interface {
	CheckConnection(ctx context.Context) (wordpress.Connection, error)
}

// newWordPressClient переводит настройки задачи в конфигурацию интеграции.
//
// Единственное место, где эти два мира встречаются. Пакет config не знает про WordPress-клиент,
// пакет wordpress не знает про переменные окружения и про задачи — связывает их composition root.
func newWordPressClient(cfg config.WordPressConfig) (*wordpress.Client, error) {
	return wordpress.NewClient(wordpress.Config{
		BaseURL:     cfg.BaseURL,
		Username:    cfg.Username,
		AppPassword: cfg.AppPassword,
		Timeout:     wordPressRequestTimeout,
		Retry:       wordpress.DefaultRetryPolicy(),
	})
}

// runWordPressCheck подтверждает, что credentials задачи рабочие, ничего не создавая.
//
// Запрос ровно один и только GET. Проверять доступ публикацией боевой статьи нельзя: POST уже
// создал бы запись, и «а прошло ли частично» пришлось бы выяснять руками в админке.
//
// envPrefix нужен подсказке: у pprof_1 переменные называются PPROF_1_WORDPRESS_*, и назвать
// человеку не ту переменную, которую он правит, — значит отправить его чинить не тот файл.
func runWordPressCheck(
	ctx context.Context,
	client wordPressChecker,
	envPrefix string,
	logger *slog.Logger,
	out io.Writer,
) error {
	ctx, cancel := context.WithTimeout(ctx, wordPressCheckDeadline)
	defer cancel()

	logger.Info("проверка подключения к WordPress начата", "stage", "wordpress_check")
	connection, err := client.CheckConnection(ctx)
	if err != nil {
		if wordpress.NeedsCredentialsCheck(err) {
			return fmt.Errorf("%w\nДальше: проверьте %sWORDPRESS_USERNAME и %sWORDPRESS_APP_PASSWORD в .env",
				err, envPrefix, envPrefix)
		}
		return err
	}

	// Ни пароль, ни заголовок авторизации в лог не попадают — здесь только то, чем ответил сайт.
	logger.Info("WordPress: подключение установлено",
		"stage", "wordpress_check",
		"http_status", connection.StatusCode,
		"user_id", connection.User.ID,
		"user_login", connection.User.Login,
		"can_publish_posts", connection.User.CanPublishPosts)

	fmt.Fprintf(out, "WordPress отвечает: HTTP %d\n", connection.StatusCode)
	fmt.Fprintf(out, "Пользователь: %s (id=%d)\n", connection.User.Login, connection.User.ID)
	if connection.User.CanPublishPosts {
		fmt.Fprintln(out, "Право publish_posts: есть")
		return nil
	}
	// Не ошибка: credentials рабочие, проверка пройдена. Но знать об этом лучше сейчас, чем
	// на первом POST, когда публикация уже начата.
	fmt.Fprintln(out, "Право publish_posts: НЕТ — публиковать этим пользователем не получится")
	return nil
}

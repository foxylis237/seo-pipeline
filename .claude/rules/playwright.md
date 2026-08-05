---
paths:
  - "internal/integrations/**"
  - "internal/llm/deepseekweb/**"
---

# Браузерные интеграции

Три интеграции автоматизируют **чужие** сайты: Keys.so, Arsenkin, DeepSeek Web. Их вёрстка
меняется без предупреждения — это самая частая причина отказов в проде.

## Безопасность профиля

Persistent-профиль Chromium защищается `flock` на lock-файле внутри каталога профиля:
Arsenkin (`arsenkin/client.go`) и DeepSeek (`deepseekweb/browser.go`) так и делают.
**Keys.so — нет**, и это гонка: два параллельных `prepare` портят LevelDB профиля.

`data/` содержит живые Cookies. Не читать, не логировать, не коммитить, не передавать в MCP.

## Селекторы

Селекторы держать константами в одном месте файла или в отдельном `selectors.go`
(как у `deepseekweb`), а не размазывать по функциям.

Отладка сломанного селектора — `/selector-fix`: он открывает страницу через
Chrome DevTools MCP и проверяет кандидата `evaluate_script` вживую, вместо угадывания
по сохранённому `page.html`. Chrome DevTools MCP и `make task-1 prepare` **не запускать
одновременно** — конфликт за профиль.

## Ошибки

Классифицировать явными типами, не строками. Образец — `keysso.resultError` с
`resultNoData` / `resultMaintenance` / `resultNavigationError` / `resultTimeout` /
`resultUnexpectedPage` и полем `Retryable`.

`no_data` — окончательный ответ, повтор бессмысленен. Технические и навигационные ошибки —
ограниченный retry (`keywordsTableMaxAttempts = 3`).

## Ожидания

Никаких `sleep`. Ждать наблюдаемое состояние: `WaitForFunction` по условию на DOM,
исчезновение индикатора загрузки, стабилизацию текста (образец — `completedAnswerJS`
в `deepseekweb/selectors.go`).

## Диагностика

При неудачной попытке сохраняется `screenshot.png`, `page.html` и `info.json` в
`output/task1/debug/<integration>/article-<id>/<ts>-attempt-<n>/`.

Секреты вычищать до записи: `redactDiagnosticHTML` подменяет email и пароль, `info.json`
не содержит cookies и заголовков авторизации. Новые поля в диагностике проверять на это же.

Ротации нет — каталог растёт неограниченно (сейчас 7.2 МБ). Добавляя новую диагностику,
не увеличивать объём на попытку без необходимости.

## Контекст

`ctx` пробрасывать в ожидания и логирование. Сейчас `log()` в обеих интеграциях использует
`context.Background()` — 30 срабатываний `contextcheck`.

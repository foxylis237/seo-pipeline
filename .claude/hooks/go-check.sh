#!/bin/bash
# PostToolUse на Edit|Write. Быстрая обратная связь по Go-файлу: формат и go vet пакета.
# Не блокирует — возвращает замечания в контекст модели.
# Молчит, когда всё чисто: успех должен быть невидимым.

set -uo pipefail

payload=$(cat)
file=$(printf '%s' "$payload" | jq -r '.tool_input.file_path // .tool_response.filePath // empty')

[ -n "$file" ] || exit 0
[ "${file##*.}" = "go" ] || exit 0

root="${CLAUDE_PROJECT_DIR:-$PWD}"
cd "$root" || exit 0
[ -f "$file" ] || exit 0

# Хук получает абсолютный путь. Приводим к относительному от корня проекта:
# go vet принимает только пути пакетов, а не произвольные абсолютные каталоги.
case "$file" in
    "$root"/*) rel="${file#"$root"/}" ;;
    /*)        exit 0 ;;  # файл вне проекта — не наше дело
    *)         rel="${file#./}" ;;
esac

newline=$'\n'
problems=""

if [ -n "$(gofmt -l "$rel" 2>/dev/null)" ]; then
    problems="gofmt: файл не отформатирован, нужен make fmt"
fi

# go vet по каталогу файла — быстрее, чем по всему модулю.
# Префикс ./ обязателен: без него путь читается как пакет стандартной библиотеки.
dir="./$(dirname "$rel")"
if ! vet=$(go vet "$dir" 2>&1); then
    problems="${problems}${problems:+$newline}go vet:${newline}${vet}"
fi

[ -n "$problems" ] || exit 0

printf '%s' "$problems" | jq -Rs '{
  hookSpecificOutput: {
    hookEventName: "PostToolUse",
    additionalContext: ("Проверка Go-файла нашла замечания:\n" + .)
  }
}'

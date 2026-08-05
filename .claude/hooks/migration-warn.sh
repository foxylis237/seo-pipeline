#!/bin/bash
# PostToolUse на Edit|Write. Правка migrations/** без синхронной правки expectedSchema
# ломает запуск приложения целиком — ValidateSchema отказывает до первого запроса.
# См. docs/adr/0003-validate-schema.md.

set -uo pipefail

payload=$(cat)
file=$(printf '%s' "$payload" | jq -r '.tool_input.file_path // .tool_response.filePath // empty')

[ -n "$file" ] || exit 0
case "$file" in
    */migrations/*.sql|migrations/*.sql) ;;
    *) exit 0 ;;
esac

root="${CLAUDE_PROJECT_DIR:-$PWD}"
cd "$root" || exit 0

schema="internal/tasks/task1/repository/schema.go"
base=$(basename "$file")

# Уже поправлен в этой же незакоммиченной работе — напоминать не о чем.
if git diff --name-only HEAD 2>/dev/null | grep -qF "$schema"; then
    exit 0
fi

# Парная миграция: у каждой .up.sql должна быть .down.sql и наоборот.
pair_note=""
case "$base" in
    *.up.sql)   [ -f "${file%.up.sql}.down.sql" ] || pair_note=" Парный .down.sql отсутствует." ;;
    *.down.sql) [ -f "${file%.down.sql}.up.sql" ] || pair_note=" Парный .up.sql отсутствует." ;;
esac

jq -n --arg s "$schema" --arg p "$pair_note" '{
  hookSpecificOutput: {
    hookEventName: "PostToolUse",
    additionalContext: ("Изменена миграция. expectedSchema в " + $s +
      " требует синхронной правки — иначе ValidateSchema остановит приложение при следующем запуске." +
      " Миграции применяются только при первой инициализации volume: чтобы проверить, нужен make docker-down, удаление volume и make docker-up." + $p)
  }
}'

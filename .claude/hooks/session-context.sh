#!/bin/bash
# SessionStart. Убирает три вопроса, которые иначе задаются в начале каждой сессии:
# что в дереве, поднята ли БД, выполнятся ли тесты репозитория.

set -uo pipefail

root="${CLAUDE_PROJECT_DIR:-$PWD}"
cd "$root" || exit 0

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "нет git")
ahead=$(git status -sb 2>/dev/null | head -1 | grep -o '\[.*\]' || echo "")
dirty=$(git status --short 2>/dev/null | head -10)
[ -n "$dirty" ] || dirty="(дерево чистое)"

db=$(docker compose ps --format '{{.Service}} {{.State}}' 2>/dev/null)
[ -n "$db" ] || db="(контейнеры не подняты)"

if [ -n "${TEST_DATABASE_URL:-}" ]; then
    tests="TEST_DATABASE_URL задан — тесты репозитория выполнятся"
else
    tests="TEST_DATABASE_URL НЕ задан — 15 тестов репозитория будут молча пропущены (находка C3)"
fi

context=$(printf 'Ветка: %s %s\n\nРабочее дерево:\n%s\n\nPostgreSQL:\n%s\n\n%s' \
    "$branch" "$ahead" "$dirty" "$db" "$tests")

printf '%s' "$context" | jq -Rs '{
  hookSpecificOutput: {
    hookEventName: "SessionStart",
    additionalContext: .
  }
}'

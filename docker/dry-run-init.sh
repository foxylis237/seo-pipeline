#!/bin/sh
set -eu

# Схема task_1 (public) при первой инициализации volume. Каталог у каждой задачи свой, и
# применять их вперемешку нельзя: в схеме pprof_2 нет колонок, которые заводит task_1.
# Схемы pprof_1 и pprof_2 создаются руками — см. migrations/README.md.
for migration in /migrations/task_1/*.up.sql; do
  psql --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --file "$migration"
done

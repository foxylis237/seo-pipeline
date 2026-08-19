# Миграции схемы `task_1`

Схема живёт в `public`. Применяется автоматически при первой инициализации volume —
`docker/dry-run-init.sh` проходит по `migrations/task_1/*.up.sql` — и руками для уже
существующей базы:

```bash
docker exec -i seo-postgres psql -U seo -d seo -v ON_ERROR_STOP=1 \
  -f - < migrations/task_1/000001_schema.up.sql
```

`000001_schema.up.sql` описывает схему целиком и заменяет собой прежние четыре файла
(`000001_init_schema`, `000002_add_google_doc_url`, `000003_move_tags_to_inputs`,
`000004_add_wordpress_publication`) — их история осталась в git. Файл идемпотентен
(`IF NOT EXISTS` у всего), поэтому применение к живой базе ничего не меняет: в `public` лежат
настоящие статьи `task_1`.

Необязательные колонки, которые задача объявляет в `Profile.ExtraInputColumns`: `author`,
`links`, `professions`, `tags`. Плюс `article_metadata.tldr` — его пишет стадия `info`.
Набор совпадает с `pprof_1`: обе задачи пишут статьи блога.

Колонок `pprof_2` (`seo_title`, `section`, `profession`, `teachers`, `service_name`) здесь нет
и быть не должно: проверка схемы строгая в обе стороны.

**`000001_schema.down.sql` сносит схему вместе со статьями.** На рабочей базе не запускать.

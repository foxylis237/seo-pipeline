# Миграции схемы `pprof_2`

Применяются только к схеме `pprof_2` и только руками:

```bash
docker exec -i seo-postgres psql -U seo -d seo -v ON_ERROR_STOP=1 \
  -c 'SET search_path TO pprof_2' -f - < migrations/pprof_2/000001_schema.up.sql
```

`000001_schema.up.sql` описывает схему целиком и заменяет собой общий каталог `migrations/`.
Накатывать общие миграции поверх нельзя: они заведут `links`, `professions`, `tags` и
`article_metadata.tldr`, которых у этой задачи быть не должно, и `repository.ValidateSchema`
остановит первую же команду на «unexpected column».

Чего в схеме нет и почему:

| Колонка | Почему её нет |
|---|---|
| `article_inputs.author` | у страницы услуги автора нет; преподаватели живут в своей колонке |
| `article_inputs.links` | перелинковки у `pprof_2` нет, промпт `html` запрещает ссылки |
| `article_inputs.professions` | списка похожих профессий `result.md` не печатает |
| `article_inputs.tags` | меток в блог задача не публикует |
| `article_metadata.tldr` | TL;DR не генерируется, из разделов `info` есть только FAQ |

Свои поля — `seo_title`, `section`, `profession`, `teachers`, `service_name`. Набор обязан
совпадать с `Profile.ExtraInputColumns` в `internal/tasks/pprof2`: проверка схемы строгая в
обе стороны.

**Пересборка с нуля разрешена.** Входные данные восстанавливает повторный `make pprof-2
import` из книги, всё остальное — прогон. Поэтому baseline здесь переписывается целиком, а не
обрастает цепочкой `ADD`/`DROP`: схему сносят `000001_schema.down.sql` и создают заново.

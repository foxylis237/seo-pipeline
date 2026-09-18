package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore — каталог в своей схеме.
//
// Схема не принадлежит ни одной задаче, поэтому имя её пишется в запросах явно, а не
// приходит через search_path: тот у каждой задачи свой, и каталог, найденный «где-то в
// текущей схеме», у разных задач оказался бы разным.
//
// Имя приходит параметром и умолчания не имеет. Площадок у проекта две, каждая со своим
// каталогом в своей схеме, а Replace начинает с удаления всех услуг — «если не сказано,
// пишем в site» означало бы, что забытая настройка стирает каталог соседа. Умолчание живёт
// одним уровнем выше, в composition root, где известны и задача, и её площадка.
type PostgresStore struct {
	pool *pgxpool.Pool
	// schema — имя схемы, уже приведённое к безопасному виду: оно подставляется в текст
	// запроса, а не уходит параметром, — так схему назвать нельзя.
	schema string
}

func NewPostgresStore(pool *pgxpool.Pool, schema string) (*PostgresStore, error) {
	if strings.TrimSpace(schema) == "" {
		return nil, fmt.Errorf("не задана схема каталога услуг")
	}
	return &PostgresStore{pool: pool, schema: pgx.Identifier{schema}.Sanitize()}, nil
}

// table — полное имя таблицы каталога: «site_obuchim.programs».
func (s *PostgresStore) table(name string) string { return s.schema + "." + name }

// Replace заменяет услуги целиком, сохраняя рубрики и профессии.
//
// Услуги переписываются начисто: пропавшая с площадки программа обязана исчезнуть, иначе
// однажды уйдёт в блок под статьёй ссылкой в никуда. Рубрики, профессии и их синонимы
// переживают сбор: на профессии человек мог повесить свои синонимы (source = manual), и
// снести их вместе с услугами значило бы потерять ручную работу.
func (s *PostgresStore) Replace(ctx context.Context, programs []Program) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		industries, err := s.saveIndustries(ctx, tx, programs)
		if err != nil {
			return err
		}
		professions, err := s.saveProfessions(ctx, tx, programs)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+s.table("programs")); err != nil {
			return fmt.Errorf("очистить услуги: %w", err)
		}
		for _, program := range programs {
			_, err := tx.Exec(ctx, `
				INSERT INTO `+s.table("programs")+` (
					post_id, post_type, category, priority, slug, url, title, name,
					industry_id, profession_id, updated_at
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
			`,
				program.PostID, program.PostType, program.Category, program.Priority,
				program.Slug, program.URL, program.Title, program.Name,
				industries[industryKey(program.Industry)], professions[program.Profession.Slug],
			)
			if err != nil {
				return fmt.Errorf("сохранить услугу %d: %w", program.PostID, err)
			}
		}
		return nil
	})
}

// List читает каталог целиком.
func (s *PostgresStore) List(ctx context.Context) ([]Program, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.post_id, p.post_type, p.category, p.priority, p.slug, p.url, p.title, p.name,
		       COALESCE(i.taxonomy, ''), COALESCE(i.term_id, 0), COALESCE(i.slug, ''), COALESCE(i.name, ''),
		       COALESCE(f.slug, ''), COALESCE(f.name, ''),
		       COALESCE(ARRAY_AGG(a.alias) FILTER (WHERE a.alias IS NOT NULL), '{}')
		FROM `+s.table("programs")+` p
		LEFT JOIN `+s.table("industries")+` i ON i.id = p.industry_id
		LEFT JOIN `+s.table("professions")+` f ON f.id = p.profession_id
		LEFT JOIN `+s.table("profession_aliases")+` a ON a.profession_id = f.id
		GROUP BY p.post_id, i.taxonomy, i.term_id, i.slug, i.name, f.slug, f.name
		ORDER BY p.priority, p.name
	`)
	if err != nil {
		return nil, fmt.Errorf("прочитать каталог услуг: %w", err)
	}
	defer rows.Close()

	var programs []Program
	for rows.Next() {
		var program Program
		if err := rows.Scan(
			&program.PostID, &program.PostType, &program.Category, &program.Priority,
			&program.Slug, &program.URL, &program.Title, &program.Name,
			&program.Industry.Taxonomy, &program.Industry.TermID, &program.Industry.Slug,
			&program.Industry.Name, &program.Profession.Slug, &program.Profession.Name,
			&program.Profession.Aliases,
		); err != nil {
			return nil, fmt.Errorf("разобрать услугу каталога: %w", err)
		}
		programs = append(programs, program)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("прочитать каталог услуг: %w", err)
	}
	return programs, nil
}

// industryKey — ключ рубрики в пределах сбора: таксономия и термин площадки.
func industryKey(industry Industry) string {
	return fmt.Sprintf("%s:%d", industry.Taxonomy, industry.TermID)
}

func (s *PostgresStore) saveIndustries(ctx context.Context, tx pgx.Tx, programs []Program) (map[string]int64, error) {
	ids := make(map[string]int64)
	for _, program := range programs {
		key := industryKey(program.Industry)
		if _, done := ids[key]; done {
			continue
		}
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO `+s.table("industries")+` (taxonomy, term_id, slug, name, updated_at)
			VALUES ($1, $2, $3, $4, NOW())
			ON CONFLICT (taxonomy, term_id) DO UPDATE
			SET slug = EXCLUDED.slug, name = EXCLUDED.name, updated_at = NOW()
			RETURNING id
		`, program.Industry.Taxonomy, program.Industry.TermID, program.Industry.Slug,
			program.Industry.Name).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("сохранить рубрику %s: %w", program.Industry.Slug, err)
		}
		ids[key] = id
	}
	return ids, nil
}

// saveProfessions сохраняет наши рубрики и слова, по которым они узнаются.
//
// Синоним, заведённый разбором, обновляется; заведённый человеком — остаётся: ON CONFLICT
// здесь ничего не делает, и ручная строка переживает любой пересбор.
func (s *PostgresStore) saveProfessions(ctx context.Context, tx pgx.Tx, programs []Program) (map[string]int64, error) {
	ids := make(map[string]int64)
	for _, program := range programs {
		profession := program.Profession
		if _, done := ids[profession.Slug]; done {
			continue
		}
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO `+s.table("professions")+` (slug, name, updated_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name, updated_at = NOW()
			RETURNING id
		`, profession.Slug, profession.Name).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("сохранить профессию %s: %w", profession.Slug, err)
		}
		ids[profession.Slug] = id
		for _, alias := range profession.Aliases {
			_, err := tx.Exec(ctx, `
				INSERT INTO `+s.table("profession_aliases")+` (profession_id, alias, source)
				VALUES ($1, $2, 'catalog')
				ON CONFLICT (alias) DO NOTHING
			`, id, alias)
			if err != nil {
				return nil, fmt.Errorf("сохранить синоним %q профессии %s: %w",
					alias, profession.Slug, err)
			}
		}
	}
	return ids, nil
}

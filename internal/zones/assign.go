package zones

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AssignReport — результат привязки объектов к зонам (в области country).
type AssignReport struct {
	WithGeom  int // объектов с координатами (кандидаты на привязку)
	NoGeom    int // объектов без координат — zone_id не может быть достоверным
	// Terminal (C20): терминально «вне всех зон» — исключены из
	// кандидатов (не переобрабатываются каждый раунд впустую, C15).
	Terminal  int
	Processed int // кандидатов, прошедших spatial-join (zone_dirty + без зоны)
	Changed   int // объектов, у которых zone_id изменился (привязка/перепривязка)
	Cleared   int // объектов, у которых zone_id обнулён (не попадает ни в одну зону)
	Uncovered int // объектов с координатами, не попавших ни в одну зону
}

// Assign инкрементально привязывает объекты к зонам (ТЗ §7.1, этап 4; C9).
//
// Обрабатываются только кандидаты: объекты с изменившимися координатами
// (zone_dirty — ставит enrich при публикации новых координат и Import при
// переимпорте зон) либо с координатами, но без зоны (ещё не привязан либо
// вне всех зон — перепроверяется на каждом прогоне). country — область
// («» — все страны). Глобальный проход по всему корпусу — исходный дефект:
// 8,5 минуты и «тишина публикации» ради единиц записей.
//
// Правило: объект получает самую конкретную зону, покрывающую его точку —
// zone > municipality > region; при нескольких зонах одного уровня — ту,
// что меньше по площади (детальнее территория), затем по id (детерминизм).
// Честные NULL (ТЗ §0.4): без координат или вне всех зон — zone_id = NULL.
//
// Кандидат, покрываемый хотя бы одной зоной, после проверки снимается с
// метки dirty (привязка не изменилась — тоже результат проверки). Кандидат
// вне всех зон метку сохраняет — повторная проверка на следующем прогоне
// (зоны могли пополниться). Идемпотентно: без новых меток и новых объектов
// кандидатов нет — spatial-join не выполняется.
func Assign(ctx context.Context, pool *pgxpool.Pool, country string) (*AssignReport, error) {
	rep := &AssignReport{}
	var (
		whereCountry string // «WHERE o.country = $1» для счётчиков
		andCountry   string // «AND o.country = $1» внутри WHERE
		andWhere     string // «AND o.country = $1» внутри CTE
		args         []any
	)
	if country != "" {
		whereCountry = " WHERE o.country = $1"
		andCountry = "AND o.country = $1 "
		andWhere = "\n      AND o.country = $1"
		args = []any{country}
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE o.geom IS NOT NULL),
		       count(*) FILTER (WHERE o.geom IS NULL),
		       count(*) FILTER (WHERE o.zone_terminal)
		FROM objects o`+whereCountry, args...).Scan(&rep.WithGeom, &rep.NoGeom, &rep.Terminal); err != nil {
		return nil, fmt.Errorf("zones assign: подсчёт объектов: %w", err)
	}

	err := withTx(ctx, pool, func(tx pgx.Tx) error {
		// Кандидаты: изменились координаты либо есть координаты, но нет зоны.
		// Терминальные (C20) — исключены: «вне всех зон» окончательный,
		// повторная проверка каждый раунд всегда впустую (C15).
		cands := `
			SELECT o.id, o.country, o.zone_id AS old_zone, o.geom
			FROM objects o
			WHERE o.geom IS NOT NULL
			  AND NOT o.zone_terminal
			  AND (o.zone_dirty OR o.zone_id IS NULL)` + andWhere + `
		`

		// 1) Привязка + снятие метки одним spatial-join: каждый кандидат
		//    получает самую конкретную покрывающую зону либо NULL (вне всех
		//    зон — zone_id обнуляется честно, метка dirty сохраняется).
		rows, err := tx.Query(ctx, `
			WITH candidates AS (`+cands+`),
			ranked AS (
				SELECT c.id AS object_id, c.old_zone, z.id AS zone_id,
				       ROW_NUMBER() OVER (
				           PARTITION BY c.id
				           ORDER BY CASE z.level
					                    WHEN 'zone' THEN 0
					                    WHEN 'municipality' THEN 1
					                    ELSE 2
					                END,
					                   ST_Area(z.geom),
					                   z.id
				       ) AS rn
				FROM candidates c
				JOIN zones z ON z.country = c.country AND ST_Covers(z.geom, c.geom)
			),
			best AS (
				SELECT c.id AS object_id,
				       c.old_zone,
				       r.zone_id AS new_zone
				FROM candidates c
				LEFT JOIN ranked r ON r.object_id = c.id AND r.rn = 1
			)
			UPDATE objects o
			SET zone_id = b.new_zone,
			    zone_dirty = CASE WHEN b.new_zone IS NOT NULL THEN FALSE ELSE o.zone_dirty END
			FROM best b
			WHERE o.id = b.object_id
			RETURNING b.new_zone IS NOT NULL AND b.new_zone IS DISTINCT FROM b.old_zone,
			          b.old_zone IS NOT NULL AND b.new_zone IS NULL`, args...)
		if err != nil {
			return fmt.Errorf("zones assign: привязка: %w", err)
		}
		for rows.Next() {
			var changed, cleared bool
			if err := rows.Scan(&changed, &cleared); err != nil {
				return fmt.Errorf("zones assign: чтение результата: %w", err)
			}
			// best LEFT JOIN: одна строка на кандидата — это и есть
			// прошедшие spatial-join (C15: отчёт «обработано»).
			rep.Processed++
			if changed {
				rep.Changed++
			}
			if cleared {
				rep.Cleared++
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("zones assign: привязка: %w", err)
		}
		rows.Close()

		// 2) Координат больше нет, а зона записана — честный NULL.
		//    Дёшево (без spatial-join): таких строк в штатной работе нет.
		tag, err := tx.Exec(ctx, `
			UPDATE objects o
			SET zone_id = NULL
			WHERE o.zone_id IS NOT NULL AND o.geom IS NULL `+andCountry, args...)
		if err != nil {
			return fmt.Errorf("zones assign: обнуление без координат: %w", err)
		}
		rep.Cleared += int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return nil, err
	}

	uncoveredQ := "SELECT count(*) FROM objects o WHERE o.geom IS NOT NULL AND o.zone_id IS NULL"
	if country != "" {
		uncoveredQ += " AND o.country = $1"
	}
	if err := pool.QueryRow(ctx, uncoveredQ, args...).Scan(&rep.Uncovered); err != nil {
		return nil, fmt.Errorf("zones assign: подсчёт непокрытых: %w", err)
	}
	return rep, nil
}

package zones

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AssignReport — результат привязки объектов к зонам.
type AssignReport struct {
	WithGeom  int // объектов с координатами (кандидаты на привязку)
	NoGeom    int // объектов без координат — zone_id не может быть достоверным
	Changed   int // объектов, у которых zone_id изменился (привязка/перепривязка)
	Cleared   int // объектов, у которых zone_id обнулён (не попадает ни в одну зону)
	Uncovered int // объектов с координатами, не попавших ни в одну зону
}

// Assign привязывает объекты к зонам через PostGIS (ТЗ §7.1, этап 4).
//
// Правило: объект получает самую конкретную зону, покрывающую его точку —
// zone > municipality > region; при нескольких зонах одного уровня — ту,
// что меньше по площади (детальнее территория), затем по id (детерминизм).
// Честные NULL (ТЗ §0.4): без координат или вне всех зон — zone_id = NULL.
//
// Идемпотентно: повторно запущенный assign без изменений данных ничего не
// меняет (Changed = 0, Cleared = 0). Статус объекта не имеет значения —
// зона описывает расположение, а не актуальность.
func Assign(ctx context.Context, pool *pgxpool.Pool) (*AssignReport, error) {
	rep := &AssignReport{}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE geom IS NOT NULL),
		       count(*) FILTER (WHERE geom IS NULL)
		FROM objects`).Scan(&rep.WithGeom, &rep.NoGeom); err != nil {
		return nil, fmt.Errorf("zones assign: подсчёт объектов: %w", err)
	}

	err := withTx(ctx, pool, func(tx pgx.Tx) error {
		// 1) Самая конкретная покрывающая зона для каждого объекта с точкой.
		tag, err := tx.Exec(ctx, `
			WITH ranked AS (
				SELECT o.id AS object_id,
				       z.id  AS zone_id,
				       ROW_NUMBER() OVER (
				           PARTITION BY o.id
				           ORDER BY CASE z.level
					                    WHEN 'zone' THEN 0
					                    WHEN 'municipality' THEN 1
					                    ELSE 2
					                END,
					                   ST_Area(z.geom),
					                   z.id
				       ) AS rn
				FROM objects o
				JOIN zones z ON z.country = o.country AND ST_Covers(z.geom, o.geom)
				WHERE o.geom IS NOT NULL
			)
			UPDATE objects o
			SET zone_id = r.zone_id
			FROM (SELECT object_id, zone_id FROM ranked WHERE rn = 1) r
			WHERE o.id = r.object_id
			  AND o.zone_id IS DISTINCT FROM r.zone_id`)
		if err != nil {
			return fmt.Errorf("zones assign: привязка: %w", err)
		}
		rep.Changed = int(tag.RowsAffected())

		// 2) Обнулить: зона записана, но объект в неё не попадает (данные
		//    зон обновлены) либо координат больше нет.
		tag, err = tx.Exec(ctx, `
			UPDATE objects o
			SET zone_id = NULL
			WHERE o.zone_id IS NOT NULL
			  AND (o.geom IS NULL
			       OR NOT EXISTS (SELECT 1 FROM zones z
			                       WHERE z.id = o.zone_id AND ST_Covers(z.geom, o.geom)))`)
		if err != nil {
			return fmt.Errorf("zones assign: обнуление: %w", err)
		}
		rep.Cleared = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM objects WHERE geom IS NOT NULL AND zone_id IS NULL`).
		Scan(&rep.Uncovered); err != nil {
		return nil, fmt.Errorf("zones assign: подсчёт непокрытых: %w", err)
	}
	return rep, nil
}

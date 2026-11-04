package zones

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LinkReport — результат связывания родителей по геометрии.
type LinkReport struct {
	Candidates int // зон уровня без parent_id на входе
	Linked     int // parent_id установлено
	Unlinked   int // покрывающая родительская зона не найдена
}

// LinkByGeometry ставит parent_id зонам уровня level (municipality|zone),
// у которых родитель ещё не задан: родителем считается зона требуемого
// уровня, содержащая точку поверхности полигона ребёнка
// (ST_PointOnSurface гарантированно лежит на поверхности, ST_Covers
// учитывает случай точки на общей границе). Кандидатов несколько
// (перекрытие границ в исходных данных) — берём наименьшую по площади,
// то есть более специфичную; при равенстве — меньший id (детерминизм).
//
// Пространственные функции считаются в домене geometry
// (геометрии зон — WGS84, каст geography::geometry точен): в локальной
// сборке PostGIS отсутствуют перегрузки ST_PointOnSurface(geography) и
// ряд других гео-функций (см. заголовок пакета geojson.go). Плоская
// площадь ST_Area(geometry) пригодна для ранжирования «меньше —
// специфичнее» в пределах одного уровня.
//
// Используется для источников, не дающих родительских ссылок в данных
// (например geoBoundaries: уровни ADM2/ADM4 без parent). Родительский
// уровень обязан быть импортирован до вызова. Идемпотентно: повторно
// запускается только над зонами с parent_id IS NULL.
func LinkByGeometry(ctx context.Context, pool *pgxpool.Pool, country, level string) (*LinkReport, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 {
		return nil, fmt.Errorf("zones: country: код из двух букв, получено %q", country)
	}
	want, ok := validLevels[level]
	if !ok || want == "" {
		return nil, fmt.Errorf("zones: link: уровень %q: связывают только municipality|zone (region родителя не имеет)", level)
	}

	var candidates int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM zones WHERE country = $1 AND level = $2 AND parent_id IS NULL`,
		country, level).Scan(&candidates); err != nil {
		return nil, fmt.Errorf("zones: link: подсчёт: %w", err)
	}
	if candidates == 0 {
		return &LinkReport{}, nil
	}

	res, err := pool.Exec(ctx, `
		UPDATE zones z
		SET parent_id = (
			SELECT p.id
			FROM zones p
			WHERE p.country = z.country
			  AND p.level = $1
			  AND ST_Covers(p.geom::geometry, ST_PointOnSurface(z.geom::geometry))
			ORDER BY ST_Area(p.geom::geometry), p.id
			LIMIT 1
		)
		WHERE z.country = $2 AND z.level = $3 AND z.parent_id IS NULL`,
		want, country, level)
	if err != nil {
		return nil, fmt.Errorf("zones: link: обновление parent_id: %w", err)
	}

	var unlinked int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM zones WHERE country = $1 AND level = $2 AND parent_id IS NULL`,
		country, level).Scan(&unlinked); err != nil {
		return nil, fmt.Errorf("zones: link: подсчёт остатка: %w", err)
	}
	linked := int(res.RowsAffected())
	if linked+unlinked != candidates {
		return nil, fmt.Errorf("zones: link: внутренний сбой: связано %d + не связано %d != кандидатов %d",
			linked, unlinked, candidates)
	}
	if unlinked > 0 {
		return &LinkReport{Candidates: candidates, Linked: linked, Unlinked: unlinked},
			fmt.Errorf("zones: link: %d зон без покрывающей родительской зоны — проверьте полноту верхнего уровня", unlinked)
	}
	return &LinkReport{Candidates: candidates, Linked: linked}, nil
}

// Package zones — этап 4: полигоны зон (ТЗ §7.1) и базовые уровни цены по зонам.
//
// Геометрии импортируются чистым Go, без GDAL/CGO: GeoJSON передаётся в
// PostGIS как есть (ST_Multi(ST_GeomFromGeoJSON(...))::geography — в локальной
// сборке PostGIS отсутствуют ST_GeogFromGeoJSON и ST_Multi(geography)),
// локальный разбор координат не нужен. Это важно для конечной цели —
// Termux на vzu5-omi (без CGO).
package zones

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// validLevels — уровни зон (ТЗ §7.1: country → region → municipality → zone;
// уровень country — не строка в zones). Значение — требуемый уровень родителя.
var validLevels = map[string]string{
	"region":       "",
	"municipality": "region",
	"zone":         "municipality",
}

// gjFeature — фича GeoJSON на входе.
type gjFeature struct {
	Type       string          `json:"type"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties json.RawMessage `json:"properties"`
}

// inFeature — зона из GeoJSON-файла, прошедшая валидацию.
type inFeature struct {
	index        int
	level        string
	name         string
	externalCode *string
	parentCode   *string
	geomJSON     string
}

// ImportReport — результат импорта GeoJSON.
type ImportReport struct {
	Features         int      // фичей в файле
	Inserted         int      // строк создано
	Updated          int      // строк обновлено (тот же country+external_code)
	NoCode           int      // фич без external_code — всегда создаются заново
	Unresolved       int      // parent_external_code, не найденных в zones
	UnresolvedSample []string // до 10 таких кодов
}

// Import загружает зоны из GeoJSON FeatureCollection (ТЗ §7.1).
//
// Свойства фичи: level (region|municipality|zone), name (непустое),
// external_code (необязательное), parent_external_code (только для
// municipality/zone). Геометрия — Polygon или MultiPolygon, WGS84.
//
// Идемпотентность: (country, external_code) уникален — повторный импорт того
// же файла обновляет строки. Весь импорт — одна транзакция; при любой ошибке
// ничего не записывается, кроме заведомо валидных данных, которые откатываются.
//
// Нерешённые родительские ссылки не откатывают импорт (зона без ссылки
// пригодна для привязки объектов; иерархия нужна для zone_effect на этапе 5),
// но сообщаются: функция возвращает и отчёт, и ошибку с кодами.
func Import(ctx context.Context, pool *pgxpool.Pool, path, country, source string) (*ImportReport, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 {
		return nil, fmt.Errorf("zones: country: код из двух букв, получено %q", country)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("zones: source: имя источника обязательно (атрибуция, ТЗ §13)")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("zones: чтение %s: %w", path, err)
	}
	var fc struct {
		Type     string      `json:"type"`
		Features []gjFeature `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("zones: GeoJSON: %w", err)
	}
	if fc.Type != "FeatureCollection" {
		return nil, fmt.Errorf("zones: GeoJSON: верхний объект %q, требуется FeatureCollection", fc.Type)
	}
	if len(fc.Features) == 0 {
		return nil, fmt.Errorf("zones: GeoJSON: в файле нет ни одной фичи")
	}

	feats := make([]inFeature, 0, len(fc.Features))
	codes := map[string]int{} // external_code → индекс фичи (дубли внутри файла — ошибка)
	for i, f := range fc.Features {
		fe, err := parseFeature(i, f)
		if err != nil {
			return nil, err
		}
		if fe.externalCode != nil {
			if prev, dup := codes[*fe.externalCode]; dup {
				return nil, fmt.Errorf("zones: фича %d: дублируется external_code %q (фича %d)",
					i, *fe.externalCode, prev)
			}
			codes[*fe.externalCode] = i
		}
		feats = append(feats, fe)
	}

	// Существующие коды страны — до/после для учёта created/updated.
	existing := map[string]bool{}
	rows, err := pool.Query(ctx,
		`SELECT external_code FROM zones WHERE country = $1 AND external_code IS NOT NULL`, country)
	if err != nil {
		return nil, fmt.Errorf("zones: чтение существующих зон: %w", err)
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			return nil, err
		}
		existing[code] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rep := &ImportReport{Features: len(feats)}
	ids := make([]int64, len(feats))
	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		// Пасс 1: вставка/обновление строк.
		ids, err = insertFeatures(ctx, tx, country, source, feats)
		if err != nil {
			return err
		}
		for _, fe := range feats {
			if fe.externalCode == nil {
				rep.NoCode++
				rep.Inserted++
			} else if existing[*fe.externalCode] {
				rep.Updated++
			} else {
				rep.Inserted++
				existing[*fe.externalCode] = true
			}
		}
		// Пасс 2: parent_id по external_code родителя (по всей таблице страны).
		return linkParents(ctx, tx, country, feats, ids, rep)
	})
	if err != nil {
		return nil, err
	}
	if rep.Unresolved > 0 {
		sample := rep.UnresolvedSample
		if len(sample) > 10 {
			sample = sample[:10]
		}
		return rep, fmt.Errorf("zones: %d родительских ссылок не решено (примеры: %s) — иерархия неполна, данные записаны",
			rep.Unresolved, strings.Join(sample, ", "))
	}
	return rep, nil
}

// insertFeatures — вставка фич пачками; при ошибке пачки — построчно,
// чтобы в ошибке был номер фичи. Порядок RETURNING совпадает с порядком
// VALUES (Postgres обрабатывает строки VALUES последовательно).
func insertFeatures(ctx context.Context, tx pgx.Tx, country, source string,
	feats []inFeature) (ids []int64, err error) {
	const chunk = 100
	ids = make([]int64, len(feats))
	for start := 0; start < len(feats); start += chunk {
		end := start + chunk
		if end > len(feats) {
			end = len(feats)
		}
		if err := insertChunk(ctx, tx, country, source, feats[start:end], ids[start:end]); err != nil {
			// Пачка — одна инструкция: при ошибке ни одна её строка не
			// записана. Повторяем построчно, чтобы указать, какая фича сломалась.
			var firstErr error
			for i := start; i < end; i++ {
				if firstErr == nil {
					firstErr = insertOne(ctx, tx, country, source, feats[i], &ids[i])
				}
			}
			if firstErr == nil {
				return nil, fmt.Errorf("zones: вставка пачки %d..%d: %w (повтор построчно не воспроизвёл ошибку)", start, end, err)
			}
			// Если повтор провален самим прерыванием транзакции (SQLSTATE 25P02),
			// настоящая причина — ошибка пачки, а не конкретной фичи (ТЗ §0.2).
			if strings.Contains(firstErr.Error(), "25P02") {
				return nil, fmt.Errorf("zones: вставка пачки %d..%d: %w", start, end, err)
			}
			return nil, firstErr
		}
	}
	return ids, nil
}

// upsertZoneSQL — вставка/обновление одной зоны (ссылки $1..$6).
const upsertZoneSQL = `
	INSERT INTO zones (country, level, external_code, name, geom, source)
	VALUES ($1, $2, $3, $4, ST_Multi(ST_GeomFromGeoJSON($5::jsonb))::geography, $6)
	ON CONFLICT (country, external_code) DO UPDATE SET
		level     = EXCLUDED.level,
		name      = EXCLUDED.name,
		geom      = EXCLUDED.geom,
		source    = EXCLUDED.source,
		parent_id = NULL
	RETURNING id`

func insertOne(ctx context.Context, tx pgx.Tx, country, source string, fe inFeature, id *int64) error {
	err := tx.QueryRow(ctx, upsertZoneSQL,
		country, fe.level, fe.externalCode, fe.name, fe.geomJSON, source).Scan(id)
	if err != nil {
		return fmt.Errorf("zones: фича %d (%s %q): %w", fe.index, fe.level, fe.name, err)
	}
	return nil
}

// insertChunk — вставка пачки; ids — срез []int64 того же размера, сюда
// записываются id в порядке VALUES.
func insertChunk(ctx context.Context, tx pgx.Tx, country, source string, feats []inFeature, ids []int64) error {
	if len(ids) != len(feats) {
		return fmt.Errorf("zones: внутренний сбой: len(ids)=%d != len(feats)=%d", len(ids), len(feats))
	}
	q := "INSERT INTO zones (country, level, external_code, name, geom, source) VALUES "
	args := make([]any, 0, len(feats)*6)
	for i, fe := range feats {
		if i > 0 {
			q += ", "
		}
		b := i*6 + 1
		q += fmt.Sprintf("($%d, $%d, $%d, $%d, ST_Multi(ST_GeomFromGeoJSON($%d::jsonb))::geography, $%d)",
			b, b+1, b+2, b+3, b+4, b+5)
		args = append(args, country, fe.level, fe.externalCode, fe.name, fe.geomJSON, source)
	}
	q += ` ON CONFLICT (country, external_code) DO UPDATE SET
		level = EXCLUDED.level, name = EXCLUDED.name, geom = EXCLUDED.geom,
		source = EXCLUDED.source, parent_id = NULL
		RETURNING id`
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if n >= len(feats) {
			return fmt.Errorf("zones: RETURNING вернул больше строк, чем в пачке (%d)", len(feats))
		}
		ids[n] = id
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n != len(feats) {
		return fmt.Errorf("zones: записано строк %d вместо %d", n, len(feats))
	}
	return nil
}

// linkParents — вторая часть импорта: parent_id по external_code родителя.
// Родитель ищется по всей таблице страны (он мог прийти в предыдущем импорте).
func linkParents(ctx context.Context, tx pgx.Tx, country string,
	feats []inFeature, ids []int64, rep *ImportReport) error {
	// Все коды страны: код → (id, level).
	codeMap := map[string]struct {
		id    int64
		level string
	}{}
	rows, err := tx.Query(ctx,
		`SELECT external_code, id, level FROM zones WHERE country = $1 AND external_code IS NOT NULL`, country)
	if err != nil {
		return fmt.Errorf("zones: чтение кодов для ссылок: %w", err)
	}
	for rows.Next() {
		var code, level string
		var id int64
		if err := rows.Scan(&code, &id, &level); err != nil {
			rows.Close()
			return err
		}
		codeMap[code] = struct {
			id    int64
			level string
		}{id: id, level: level}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Группируем id детей по id родителя.
	children := map[int64][]int64{}
	for i, fe := range feats {
		if fe.parentCode == nil {
			continue
		}
		parent, ok := codeMap[*fe.parentCode]
		if !ok {
			rep.Unresolved++
			rep.UnresolvedSample = append(rep.UnresolvedSample, *fe.parentCode)
			continue
		}
		want := validLevels[fe.level]
		if parent.level != want {
			return fmt.Errorf("zones: фича %d (%s %q): родитель %q уровня %q, ожидается уровень %q",
				fe.index, fe.level, fe.name, *fe.parentCode, parent.level, want)
		}
		children[parent.id] = append(children[parent.id], ids[i])
	}
	for parentID, childIDs := range children {
		if _, err := tx.Exec(ctx,
			`UPDATE zones SET parent_id = $1 WHERE country = $2 AND id = ANY($3)`,
			parentID, country, childIDs); err != nil {
			return fmt.Errorf("zones: обновление parent_id: %w", err)
		}
	}
	return nil
}

// parseFeature — валидация одной фичи. Все ошибки — явные, с номером фичи (ТЗ §0.2).
func parseFeature(i int, f gjFeature) (inFeature, error) {
	if f.Type != "Feature" {
		return inFeature{}, fmt.Errorf("zones: фича %d: type %q, требуется Feature", i, f.Type)
	}
	var g struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(f.Geometry, &g); err != nil {
		return inFeature{}, fmt.Errorf("zones: фича %d: геометрия: %w", i, err)
	}
	if g.Type != "Polygon" && g.Type != "MultiPolygon" {
		return inFeature{}, fmt.Errorf("zones: фича %d: тип геометрии %q, допустимы Polygon/MultiPolygon", i, g.Type)
	}
	var p struct {
		Level              *string `json:"level"`
		Name               *string `json:"name"`
		ExternalCode       *string `json:"external_code"`
		ParentExternalCode *string `json:"parent_external_code"`
	}
	// properties может отсутствовать или быть null — это ошибка ниже, по level.
	if len(f.Properties) > 0 && string(f.Properties) != "null" {
		if err := json.Unmarshal(f.Properties, &p); err != nil {
			return inFeature{}, fmt.Errorf("zones: фича %d: properties: %w", i, err)
		}
	}
	if p.Level == nil || strings.TrimSpace(*p.Level) == "" {
		return inFeature{}, fmt.Errorf("zones: фича %d: properties.level отсутствует (region|municipality|zone)", i)
	}
	level := strings.ToLower(strings.TrimSpace(*p.Level))
	if _, ok := validLevels[level]; !ok {
		return inFeature{}, fmt.Errorf("zones: фича %d: level %q, допустимы region|municipality|zone", i, *p.Level)
	}
	if p.Name == nil || strings.TrimSpace(*p.Name) == "" {
		return inFeature{}, fmt.Errorf("zones: фича %d: properties.name отсутствует", i)
	}
	fe := inFeature{
		index:    i,
		level:    level,
		name:     strings.TrimSpace(*p.Name),
		geomJSON: string(f.Geometry),
	}
	if p.ExternalCode != nil && strings.TrimSpace(*p.ExternalCode) != "" {
		fe.externalCode = ptrStr(strings.TrimSpace(*p.ExternalCode))
	}
	if p.ParentExternalCode != nil && strings.TrimSpace(*p.ParentExternalCode) != "" {
		if level == "region" {
			return inFeature{}, fmt.Errorf("zones: фича %d: у region нет родителя (уровень country — не строка в zones)", i)
		}
		fe.parentCode = ptrStr(strings.TrimSpace(*p.ParentExternalCode))
	}
	return fe, nil
}

func ptrStr(s string) *string { return &s }

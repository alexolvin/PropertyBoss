package api

// U30 (TZ UI v5): эндпоинт карты — points/cells/areas для bbox вьюпорта
// со всеми фильтрами U27. Выгрузка всего корпуса не выполняется ни при
// каких параметрах (TZ v4 §9): dense-вьюпорты агрегируются в cells,
// area-объекты — в зоны.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// mapPoint — объект-точка (building/street) в режиме points.
type mapPoint struct {
	ID           int64   `json:"id"`
	Lat          float64 `json:"lat"`
	Lng          float64 `json:"lng"`
	DevClass     string  `json:"dev_class"`
	HasValuation bool    `json:"has_valuation"`
}

// mapCell — агрегированная ячейка point-объектов (ST_SnapToGrid), режим cells.
type mapCell struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Count    int64   `json:"count"`
	DevClass *string `json:"dev_class"`
	Mixed    bool    `json:"mixed"`
}

// mapArea — зональная группа area-объектов (заполняется всегда,
// независимо от режима; точками не отдаются ни при каком total).
type mapArea struct {
	ZoneID   int64   `json:"zone_id"`
	Count    int64   `json:"count"`
	DevClass *string `json:"dev_class"`
	Mixed    bool    `json:"mixed"`
}

// devClassCount — (класс, количество) для подсчёта большинства.
type devClassCount struct {
	DevClass string `json:"dc"`
	Count    int64  `json:"n"`
}

// devClassOf — единственный источник границ классов отклонения (доли,
// не проценты). Ключи определены Волной 3 (в ТЗ только русские имена):
// far_below ≤ −15 %, below (−15, −5), near [−5, 5], above (5, 15),
// far_above ≥ 15 %, none — отклонения нет. SQL-дубли: devClassFilter
// (фильтр U27) и mapDevCase (агрегация U30) — те же числа.
func devClassOf(d *float64) string {
	if d == nil {
		return "none"
	}
	switch {
	case *d <= -0.15:
		return "far_below"
	case *d < -0.05:
		return "below"
	case *d <= 0.05:
		return "near"
	case *d < 0.15:
		return "above"
	default:
		return "far_above"
	}
}

// mapDevCase — класс отклонения в SQL (границы — devClassOf).
const mapDevCase = `CASE
		WHEN v.price_deviation IS NULL THEN 'none'
		WHEN v.price_deviation <= -0.15 THEN 'far_below'
		WHEN v.price_deviation < -0.05 THEN 'below'
		WHEN v.price_deviation <= 0.05 THEN 'near'
		WHEN v.price_deviation < 0.15 THEN 'above'
		ELSE 'far_above'
	END`

// mapLatestValuation — последняя оценка (тот же LATERAL-джойн, что в списке).
const mapLatestValuation = `LEFT JOIN LATERAL (
		SELECT v2.model_version, v2.price_deviation FROM valuations v2
		WHERE v2.object_id = objects.id
		ORDER BY v2.computed_at DESC, v2.id DESC
		LIMIT 1
	) v ON true`

// mapBaseFrom — FROM для карты: dжойн оценки обязателен (dev_class
// фильтров U27 и агрегация ссылаются на v.*).
const mapBaseFrom = " FROM objects " + mapLatestValuation

// mapBBox — вьюпорт (градусы WGS84: lng — x, lat — y).
type mapBBox struct {
	minLng, minLat, maxLng, maxLat float64
}

func (b mapBBox) width() float64  { return b.maxLng - b.minLng }
func (b mapBBox) height() float64 { return b.maxLat - b.minLat }

func parseBBox(raw string) (mapBBox, error) {
	if raw == "" {
		return mapBBox{}, httpError(http.StatusBadRequest, "bbox: обязателен параметр minLng,minLat,maxLng,maxLat")
	}
	p := strings.Split(raw, ",")
	if len(p) != 4 {
		return mapBBox{}, httpError(http.StatusBadRequest, "bbox: формат minLng,minLat,maxLng,maxLat")
	}
	v := make([]float64, 4)
	for i, s := range p {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return mapBBox{}, httpError(http.StatusBadRequest, "bbox: координата %d не число", i+1)
		}
		v[i] = f
	}
	b := mapBBox{v[0], v[1], v[2], v[3]}
	if b.minLng > b.maxLng || b.minLat > b.maxLat {
		return mapBBox{}, httpError(http.StatusBadRequest, "bbox: minLng > maxLng или minLat > maxLat")
	}
	if b.minLng < -180 || b.maxLng > 180 || b.minLat < -90 || b.maxLat > 90 {
		return mapBBox{}, httpError(http.StatusBadRequest, "bbox: координаты вне диапазона (lng −180..180, lat −90..90)")
	}
	return b, nil
}

func parseZoom(raw string) (int, error) {
	if raw == "" {
		return 0, httpError(http.StatusBadRequest, "zoom: обязателен целочисленный 0..22")
	}
	z, err := strconv.Atoi(raw)
	if err != nil || z < 0 || z > 22 {
		return 0, httpError(http.StatusBadRequest, "zoom: целое 0..22")
	}
	return z, nil
}

// cellSize — размер ячейки cells в градусах: 8 ячеек на ширину 256 px
// плитки → видимый фрагмент смартфона (~1.7 плитки) составляет ~13×13
// ячеек при любом zoom (решение Волны 3, обоснование — в отчёте).
func cellSize(zoom int) float64 {
	return 360.0 / math.Pow(2, float64(zoom)) / 8
}

// majorityDev — класс большинства; при равенстве максимумов — (nil, true):
// mixed = true, dev_class = null (контракт U30).
func majorityDev(classes []devClassCount) (*string, bool) {
	if len(classes) == 0 {
		return nil, true
	}
	best := classes[0]
	for _, c := range classes[1:] {
		if c.Count > best.Count || (c.Count == best.Count && c.DevClass < best.DevClass) {
			best = c
		}
	}
	ties := 0
	for _, c := range classes {
		if c.Count == best.Count {
			ties++
		}
	}
	if ties > 1 {
		return nil, true
	}
	dc := best.DevClass
	return &dc, false
}

// parsePointWKT — "POINT(lng lat)" → (lng, lat).
func parsePointWKT(s string) (lng, lat float64, ok bool) {
	inner := strings.TrimSuffix(strings.TrimPrefix(s, "POINT("), ")")
	p := strings.Fields(inner)
	if len(p) != 2 {
		return 0, 0, false
	}
	x, err1 := strconv.ParseFloat(p[0], 64)
	y, err2 := strconv.ParseFloat(p[1], 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return x, y, true
}

// GET /api/objects/map?bbox=minLng,minLat,maxLng,maxLat&zoom=N — U30.
// Режим (Р4): total ≤ 500 → points (cells пуст), иначе → cells
// (points пуст). areas — всегда. Отсечка широкого bbox при низком zoom —
// 400 (значения предела/порога — решение Волны 3, в отчёте).
func (s *Server) handleMapObjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	for name := range r.URL.Query() {
		if !listParams[name] && name != "bbox" && name != "zoom" {
			writeErr(w, httpError(http.StatusBadRequest, "неизвестный параметр: %s", name))
			return
		}
	}

	bbox, err := parseBBox(r.URL.Query().Get("bbox"))
	if err != nil {
		writeErr(w, err)
		return
	}
	zoom, err := parseZoom(r.URL.Query().Get("zoom"))
	if err != nil {
		writeErr(w, err)
		return
	}
	maxDeg := s.Cfg.Dashboard.Map.MaxBBoxDeg
	minZ := s.Cfg.Dashboard.Map.MinZoomWide
	if (bbox.width() > maxDeg || bbox.height() > maxDeg) && zoom < minZ {
		writeErr(w, httpError(http.StatusBadRequest,
			"область слишком велика: bbox %g°×%g° при zoom %d — нужен zoom ≥ %d для bbox шире %g°",
			bbox.width(), bbox.height(), zoom, minZ, maxDeg))
		return
	}

	// Бокс через оператор && + ST_MakeEnvelope (geography) — идёт по
	// GIST-индексу objects_geom_idx (приёмка: EXPLAIN).
	var conds []string
	var args []any
	// Сфера: integer 0 (установленная сборка PostGIS 3.4.2 не имеет
	// перегрузки с boolean-сфероидом), касты — pgx шлёт параметры
	// unknown, ::geography — оператор && только geography/geography,
	// смешанного geometry/geography нет.
	conds = append(conds, "objects.geom && ST_MakeEnvelope($1::float8, $2::float8, $3::float8, $4::float8, 0)::geography")
	args = append(args, bbox.minLng, bbox.minLat, bbox.maxLng, bbox.maxLat)
	if err := listFilters(r, &conds, &args); err != nil {
		writeErr(w, err)
		return
	}
	where := " WHERE " + strings.Join(conds, " AND ")

	var total int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*)"+mapBaseFrom+where, args...).Scan(&total); err != nil {
		writeErr(w, err)
		return
	}

	points := []mapPoint{}
	cells := []mapCell{}
	areas := []mapArea{}

	pointsMax := s.Cfg.Dashboard.Map.PointsMax
	if pointsMax <= 0 {
		pointsMax = 500
	}
	if total <= pointsMax {
		rows, err := s.Pool.Query(ctx,
			"SELECT objects.id, ST_Y(objects.geom::geometry), ST_X(objects.geom::geometry), v.model_version, v.price_deviation"+
				mapBaseFrom+where+" AND objects.geo_confidence IN ('building', 'street') ORDER BY objects.id",
			args...)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var p mapPoint
			var mv *string
			var dev *float64
			if err := rows.Scan(&p.ID, &p.Lat, &p.Lng, &mv, &dev); err != nil {
				writeErr(w, err)
				return
			}
			p.DevClass = devClassOf(dev)
			p.HasValuation = mv != nil
			points = append(points, p)
		}
		if err := rows.Err(); err != nil {
			writeErr(w, err)
			return
		}
	} else {
		cellArgs := append(append([]any{}, args...), cellSize(zoom))
		rows, err := s.Pool.Query(ctx,
			"SELECT ST_AsText(g), sum(n), json_agg(json_build_object('dc', dc, 'n', n))"+
				" FROM ( SELECT ST_SnapToGrid(objects.geom::geometry, $"+strconv.Itoa(len(cellArgs))+") AS g, "+mapDevCase+
				" AS dc, count(*) AS n"+mapBaseFrom+
				where+" AND objects.geo_confidence IN ('building', 'street') GROUP BY 1, 2 ) s GROUP BY g",
			cellArgs...)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var gText string
			var n int64
			var classesJSON []byte
			if err := rows.Scan(&gText, &n, &classesJSON); err != nil {
				writeErr(w, err)
				return
			}
			var classes []devClassCount
			if err := json.Unmarshal(classesJSON, &classes); err != nil {
				writeErr(w, fmt.Errorf("map: cells: %w", err))
				return
			}
			lng, lat, ok := parsePointWKT(gText)
			if !ok {
				writeErr(w, fmt.Errorf("map: cells: не распознан WKT %q", gText))
				return
			}
			dc, mixed := majorityDev(classes)
			cells = append(cells, mapCell{Lat: lat, Lng: lng, Count: n, DevClass: dc, Mixed: mixed})
		}
		if err := rows.Err(); err != nil {
			writeErr(w, err)
			return
		}
	}

	// areas — всегда: area-объекты (ниже уровня «улица») по зонам.
	// Объект area без zone_id в группы не входит (некем группировать —
	// решение Волны 3, в отчёте).
	rows, err := s.Pool.Query(ctx,
		"SELECT objects.zone_id, "+mapDevCase+" AS dc, count(*)"+
			mapBaseFrom+
			where+" AND objects.geo_confidence IN ('municipality', 'postcode', 'zone') AND objects.zone_id IS NOT NULL"+
			" GROUP BY objects.zone_id, 2 ORDER BY objects.zone_id",
		args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	byZone := map[int64][]devClassCount{}
	var zoneOrder []int64
	for rows.Next() {
		var zoneID int64
		var dc string
		var n int64
		if err := rows.Scan(&zoneID, &dc, &n); err != nil {
			writeErr(w, err)
			return
		}
		if _, ok := byZone[zoneID]; !ok {
			zoneOrder = append(zoneOrder, zoneID)
		}
		byZone[zoneID] = append(byZone[zoneID], devClassCount{DevClass: dc, Count: n})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	for _, z := range zoneOrder {
		cls := byZone[z]
		var sum int64
		for _, c := range cls {
			sum += c.Count
		}
		dc, mixed := majorityDev(cls)
		areas = append(areas, mapArea{ZoneID: z, Count: sum, DevClass: dc, Mixed: mixed})
	}

	mode := "points"
	if total > 500 {
		mode = "cells"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":   mode,
		"total":  total,
		"points": points,
		"cells":  cells,
		"areas":  areas,
	})
}

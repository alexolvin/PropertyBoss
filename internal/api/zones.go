package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// zoneOut — зона для списка дашборда. Геометрию не отдаём: PostGIS-колонка
// тяжёлая для JSON, а дашборд v1 показывает зоны списком, не картой.
type zoneOut struct {
	ID           int64   `json:"id"`
	Country      string  `json:"country"`
	Level        string  `json:"level"`
	Name         string  `json:"name"`
	ExternalCode *string `json:"external_code,omitempty"`
	ParentName   *string `json:"parent_name,omitempty"`
	Source       string  `json:"source"`
}

// GET /api/zones?country=&level=&q=&page=&per_page=
//
// sources — DISTINCT источников зон по выбранной стране: атрибуция в UI
// (ТЗ §13 — «Источник данных» показывается пользователю, а не только
// в README). Фильтр level на sources не применяется: атрибуция должна
// описывать все данные страны, а не текущий срез списка.
//
// q — поиск по названию и внешнему коду (TZ_UI v3 §6.6: «найти зону»
// решается поиском, а не листанием справочника). Поиск отключает
// постраничный просмотр: выдача ограничена 20 совпадениями — дальше
// человек уточняет запрос, а не листает страницы.
func (s *Server) handleListZones(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	page, err := queryInt(r, "page", 1, 100000)
	if err != nil {
		writeErr(w, err)
		return
	}
	perPage, err := queryInt(r, "per_page", 50, 200)
	if err != nil {
		writeErr(w, err)
		return
	}

	var conds []string
	var args []any
	if c := r.URL.Query().Get("country"); c != "" {
		conds = append(conds, fmt.Sprintf("z.country = $%d", len(args)+1))
		args = append(args, c)
	}
	if l := r.URL.Query().Get("level"); l != "" {
		if l != "region" && l != "municipality" && l != "zone" {
			writeErr(w, httpError(http.StatusBadRequest, "level: region | municipality | zone"))
			return
		}
		conds = append(conds, fmt.Sprintf("z.level = $%d", len(args)+1))
		args = append(args, l)
	}
	if q := r.URL.Query().Get("q"); q != "" {
		conds = append(conds, fmt.Sprintf("(z.name ILIKE $%d OR z.external_code ILIKE $%d)", len(args)+1, len(args)+2))
		like := "%" + q + "%"
		args = append(args, like, like)
		// Поиск выдаёт не более 20 совпадений, без страниц (TZ_UI v3 §6.6).
		page, perPage = 1, 20
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM zones z"+where, args...).Scan(&total); err != nil {
		writeErr(w, err)
		return
	}

	order := "z.country, z.level, z.name"
	var orderArgs []any
	if q := r.URL.Query().Get("q"); q != "" {
		// В поиске имя, начинающееся с запроса, — раньше прочих
		// совпадений; префикс идёт доп. параметром сразу после args,
		// поэтому номер известен до сборки listArgs. Без pg_trgm.
		order = "(z.name LIKE $" + strconv.Itoa(len(args)+1) + ") DESC, z.name, z.country"
		orderArgs = []any{q}
	}
	listArgs := append(append(append([]any{}, args...), orderArgs...), perPage, (page-1)*perPage)
	qs := `SELECT z.id, z.country, z.level, z.name, z.external_code, p.name, z.source
		FROM zones z LEFT JOIN zones p ON p.id = z.parent_id` + where +
		" ORDER BY " + order +
		" LIMIT $" + strconv.Itoa(len(listArgs)-1) +
		" OFFSET $" + strconv.Itoa(len(listArgs))
	rows, err := s.Pool.Query(ctx, qs, listArgs...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	out := []zoneOut{}
	for rows.Next() {
		var z zoneOut
		if err := rows.Scan(&z.ID, &z.Country, &z.Level, &z.Name, &z.ExternalCode,
			&z.ParentName, &z.Source); err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, z)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Атрибуция: источники зон страны (без фильтра level — см. комментарий выше).
	var srcArgs []any
	srcWhere := ""
	if c := r.URL.Query().Get("country"); c != "" {
		srcArgs = append(srcArgs, c)
		srcWhere = " WHERE z.country = $1"
	}
	srows, err := s.Pool.Query(ctx, "SELECT DISTINCT z.source FROM zones z"+srcWhere+" ORDER BY 1", srcArgs...)
	if err != nil {
		writeErr(w, err)
		return
	}
	sources := []string{}
	for srows.Next() {
		var src string
		if err := srows.Scan(&src); err != nil {
			srows.Close()
			writeErr(w, err)
			return
		}
		sources = append(sources, src)
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total": total, "page": page, "per_page": perPage,
		"zones": out, "sources": sources,
	})
}

// GET /api/zones/{id} — карточка зоны (U39): агрегаты по активным объектам.
// Деньги — целые минорные. Тренд — доля объектов с последним снижением
// цены (нужно ≥2 точек истории); иначе null + причина.
func (s *Server) handleGetZone(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, httpError(http.StatusBadRequest, "id: целое > 0"))
		return
	}
	var z zoneOut
	err = s.Pool.QueryRow(ctx, `
SELECT z.id, z.country, z.level, z.name, z.external_code, p.name, z.source
FROM zones z LEFT JOIN zones p ON p.id = z.parent_id
WHERE z.id = $1`, id).Scan(&z.ID, &z.Country, &z.Level, &z.Name, &z.ExternalCode, &z.ParentName, &z.Source)
	if err != nil {
		writeErr(w, httpError(http.StatusNotFound, "зона %d не найдена", id))
		return
	}

	var n int
	var median, avg, ppsm *int64
	var currency *string
	err = s.Pool.QueryRow(ctx, `
SELECT count(*)::int,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY current_price_minor)::bigint,
       (sum(current_price_minor) / NULLIF(count(*), 0))::bigint,
       (sum(current_price_minor) FILTER (WHERE area_sqm > 0)
          / NULLIF(sum(area_sqm) FILTER (WHERE area_sqm > 0), 0))::bigint,
       min(currency)
FROM objects
WHERE zone_id = $1 AND status = 'active'`, id).
		Scan(&n, &median, &avg, &ppsm, &currency)
	if err != nil {
		writeErr(w, err)
		return
	}

	var nHist, nDown int
	_ = s.Pool.QueryRow(ctx, `
SELECT count(*)::int,
       count(*) FILTER (WHERE p0 < p1)::int
FROM (
  SELECT (array_agg(ph.price_minor ORDER BY ph.change_at DESC))[1] AS p0,
         (array_agg(ph.price_minor ORDER BY ph.change_at DESC))[2] AS p1
  FROM price_history ph
  JOIN objects o ON o.id = ph.object_id
  WHERE o.zone_id = $1 AND o.status = 'active'
  GROUP BY ph.object_id
  HAVING count(*) >= 2
) t`, id).Scan(&nHist, &nDown)

	minN := s.Cfg.Dashboard.Map.ZoneColorMinN
	var trend *float64
	var trendReason *string
	if nHist < minN {
		r := "insufficient_history"
		trendReason = &r
	} else if nHist > 0 {
		v := float64(nDown) / float64(nHist)
		trend = &v
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"zone":               z,
		"n_objects":          n,
		"median_price_minor": median,
		"avg_price_minor":    avg,
		"avg_ppsm_minor":     ppsm,
		"currency":           currency,
		"trend_down_share":   trend,
		"trend_reason":       trendReason,
		"trend_n":            nHist,
		"zone_color_min_n":   minN,
	})
}

// GET /api/zones/geojson?ids=1,2,3 — упрощённые полигоны для слоя карты (U32/U40).
func (s *Server) handleZonesGeoJSON(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := r.URL.Query().Get("ids")
	if raw == "" {
		writeErr(w, httpError(http.StatusBadRequest, "ids: список id через запятую"))
		return
	}
	var ids []int64
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, httpError(http.StatusBadRequest, "ids: %q не целое > 0", p))
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeErr(w, httpError(http.StatusBadRequest, "ids: пусто"))
		return
	}
	if len(ids) > 200 {
		writeErr(w, httpError(http.StatusBadRequest, "ids: не больше 200 за запрос"))
		return
	}
	simp := s.Cfg.Dashboard.Map.SimplifyDeg
	if simp <= 0 {
		simp = 0.001
	}
	rows, err := s.Pool.Query(ctx, `
SELECT z.id, z.name, ST_AsGeoJSON(ST_Simplify(z.geom::geometry, $2), 5)
FROM zones z
WHERE z.id = ANY($1) AND z.geom IS NOT NULL`, ids, simp)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	feats := []map[string]any{}
	for rows.Next() {
		var id int64
		var name string
		var gj *string
		if err := rows.Scan(&id, &name, &gj); err != nil {
			writeErr(w, err)
			return
		}
		if gj == nil || *gj == "" {
			continue
		}
		var geom any
		if err := json.Unmarshal([]byte(*gj), &geom); err != nil {
			writeErr(w, err)
			return
		}
		feats = append(feats, map[string]any{
			"type":       "Feature",
			"id":         id,
			"properties": map[string]any{"id": id, "name": name},
			"geometry":   geom,
		})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type": "FeatureCollection", "features": feats,
	})
}

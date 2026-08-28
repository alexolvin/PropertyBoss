package api

import (
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

// GET /api/zones?country=&level=&page=&per_page=
//
// sources — DISTINCT источников зон по выбранной стране: атрибуция в UI
// (ТЗ §13 — «Источник данных» показывается пользователю, а не только
// в README). Фильтр level на sources не применяется: атрибуция должна
// описывать все данные страны, а не текущий срез списка.
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
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM zones z"+where, args...).Scan(&total); err != nil {
		writeErr(w, err)
		return
	}

	listArgs := append(append([]any{}, args...), perPage, (page-1)*perPage)
	q := `SELECT z.id, z.country, z.level, z.name, z.external_code, p.name, z.source
		FROM zones z LEFT JOIN zones p ON p.id = z.parent_id` + where +
		" ORDER BY z.country, z.level, z.name LIMIT $" + strconv.Itoa(len(listArgs)-1) +
		" OFFSET $" + strconv.Itoa(len(listArgs))
	rows, err := s.Pool.Query(ctx, q, listArgs...)
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

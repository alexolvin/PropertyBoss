package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// searchConfigIn — входной JSON. Денежные суммы — целые минорные единицы (ТЗ §5),
// площади — строки (NUMERIC точно, без float-промежуточного).
type searchConfigIn struct {
	SourceID         *string        `json:"source_id"`
	Country          string         `json:"country"`
	DealType         string         `json:"deal_type"`
	PropertyType     *string        `json:"property_type"`
	FilterAttributes map[string]any `json:"filter_attributes"`
	MinAreaSqM       *string        `json:"min_area_sqm"`
	MaxAreaSqM       *string        `json:"max_area_sqm"`
	MinPriceMinor    *int64         `json:"min_price_minor"`
	MaxPriceMinor    *int64         `json:"max_price_minor"`
	Currency         *string        `json:"currency"`
	Active           *bool          `json:"active"`
}

type searchConfigOut struct {
	ID               int64          `json:"id"`
	SourceID         *string        `json:"source_id"`
	Country          string         `json:"country"`
	DealType         string         `json:"deal_type"`
	PropertyType     *string        `json:"property_type"`
	FilterAttributes map[string]any `json:"filter_attributes"`
	MinAreaSqM       *string        `json:"min_area_sqm"`
	MaxAreaSqM       *string        `json:"max_area_sqm"`
	MinPriceMinor    *int64         `json:"min_price_minor"`
	MaxPriceMinor    *int64         `json:"max_price_minor"`
	Currency         *string        `json:"currency"`
	Active           bool           `json:"active"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

func (s *Server) validateSearchConfig(ctx context.Context, in *searchConfigIn) error {
	knownCountry := false
	for _, c := range s.Cfg.Dashboard.Countries {
		if c == in.Country {
			knownCountry = true
			break
		}
	}
	if !knownCountry {
		return httpError(http.StatusBadRequest, "страна %q не входит в целевые рынки (конфиг)", in.Country)
	}
	knownDeal := false
	for _, d := range s.Cfg.Dashboard.DealTypes {
		if d == in.DealType {
			knownDeal = true
			break
		}
	}
	if !knownDeal {
		return httpError(http.StatusBadRequest, "тип сделки %q не поддерживается (допустимо: %v)", in.DealType, s.Cfg.Dashboard.DealTypes)
	}
	if in.MinAreaSqM != nil || in.MaxAreaSqM != nil {
		if (in.MinAreaSqM == nil) != (in.MaxAreaSqM == nil) {
			return httpError(http.StatusBadRequest, "площадь: задайте либо обе границы (min и max), либо ни одной")
		}
		minR, ok1 := new(big.Rat).SetString(*in.MinAreaSqM)
		maxR, ok2 := new(big.Rat).SetString(*in.MaxAreaSqM)
		if !ok1 || !ok2 {
			return httpError(http.StatusBadRequest, "площадь: некорректное десятичное число")
		}
		if minR.Cmp(maxR) > 0 {
			return httpError(http.StatusBadRequest, "min_area_sqm больше max_area_sqm")
		}
	}
	if in.MinPriceMinor != nil || in.MaxPriceMinor != nil {
		if (in.MinPriceMinor == nil) != (in.MaxPriceMinor == nil) {
			return httpError(http.StatusBadRequest, "цена: задайте либо обе границы (min и max), либо ни одной")
		}
		if *in.MinPriceMinor > *in.MaxPriceMinor {
			return httpError(http.StatusBadRequest, "min_price_minor больше max_price_minor")
		}
	}
	if in.MinPriceMinor != nil || in.MaxPriceMinor != nil {
		if in.Currency == nil {
			return httpError(http.StatusBadRequest, "цена задана, но не указана currency")
		}
		if _, err := s.currencyRef(ctx, *in.Currency); err != nil {
			return err
		}
	}
	// Фильтры — по реестру атрибутов (ТЗ §6: реестр — источник истины)
	return s.validateFilterAttributes(ctx, in.Country, in.FilterAttributes)
}

// validateFilterAttributes — каждый ключ обязан существовать в attribute_registry
// для страны, значение — соответствовать типу. Незнакомый ключ отклоняется
// явно (конфиг — не место для молчаливого накопления мусора, в отличие от
// attributes_unmapped у объектов, ТЗ §6).
func (s *Server) validateFilterAttributes(ctx context.Context, country string, attrs map[string]any) error {
	if len(attrs) == 0 {
		return nil
	}
	for key, value := range attrs {
		var dataType string
		var allowedRaw []byte
		err := s.Pool.QueryRow(ctx,
			"SELECT data_type, allowed_values FROM attribute_registry WHERE country = $1 AND key = $2",
			country, key).Scan(&dataType, &allowedRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpError(http.StatusBadRequest, "фильтр %q: атрибут отсутствует в реестре для страны %s", key, country)
		}
		if err != nil {
			return httpError(http.StatusInternalServerError, "фильтр %q: чтение реестра: %v", key, err)
		}
		if err := checkAttrValue(dataType, allowedRaw, value); err != nil {
			return httpError(http.StatusBadRequest, "фильтр %q: %v", key, err)
		}
	}
	return nil
}

func checkAttrValue(dataType string, allowedRaw []byte, v any) error {
	switch dataType {
	case "bool":
		if _, ok := v.(bool); !ok {
			return errors.New("ожидается bool")
		}
	case "int":
		n, ok := v.(json.Number)
		if !ok {
			return errors.New("ожидается целое число")
		}
		if _, err := strconv.ParseInt(n.String(), 10, 64); err != nil {
			return fmt.Errorf("ожидается целое число, получено %s", n.String())
		}
	case "float":
		n, ok := v.(json.Number)
		if !ok {
			return errors.New("ожидается число")
		}
		if _, err := strconv.ParseFloat(n.String(), 64); err != nil {
			return fmt.Errorf("ожидается число, получено %s", n.String())
		}
	case "enum":
		str, ok := v.(string)
		if !ok {
			return errors.New("ожидается строка из allowed_values")
		}
		var allowed []string
		if err := json.Unmarshal(allowedRaw, &allowed); err != nil {
			return fmt.Errorf("реестр: некорректные allowed_values: %v", err)
		}
		for _, a := range allowed {
			if a == str {
				return nil
			}
		}
		return fmt.Errorf("значение %q не входит в allowed_values %v", str, allowed)
	default:
		return fmt.Errorf("неизвестный data_type %q в реестре", dataType)
	}
	return nil
}

// GET /api/search-configs?country=&source_id=
func (s *Server) handleListSearchConfigs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := "SELECT id, source_id, country, deal_type, property_type, filter_attributes, " +
		"min_area_sqm, max_area_sqm, min_price_minor, max_price_minor, currency, active, created_at, updated_at " +
		"FROM search_configs"
	var conds []string
	var args []any
	if c := r.URL.Query().Get("country"); c != "" {
		conds = append(conds, fmt.Sprintf("country = $%d", len(args)+1))
		args = append(args, c)
	}
	if src := r.URL.Query().Get("source_id"); src != "" {
		conds = append(conds, fmt.Sprintf("source_id = $%d", len(args)+1))
		args = append(args, src)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY id"
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []searchConfigOut{}
	for rows.Next() {
		var c searchConfigOut
		var attrsRaw []byte
		if err := rows.Scan(&c.ID, &c.SourceID, &c.Country, &c.DealType, &c.PropertyType,
			&attrsRaw, &c.MinAreaSqM, &c.MaxAreaSqM, &c.MinPriceMinor, &c.MaxPriceMinor,
			&c.Currency, &c.Active, &c.CreatedAt, &c.UpdatedAt); err != nil {
			writeErr(w, err)
			return
		}
		if err := json.Unmarshal(attrsRaw, &c.FilterAttributes); err != nil {
			c.FilterAttributes = map[string]any{}
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/search-configs/{id}
func (s *Server) handleGetSearchConfig(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, httpError(http.StatusBadRequest, "id: не целое"))
		return
	}
	var c searchConfigOut
	var attrsRaw []byte
	err = s.Pool.QueryRow(r.Context(), `
		SELECT id, source_id, country, deal_type, property_type, filter_attributes,
		       min_area_sqm, max_area_sqm, min_price_minor, max_price_minor, currency, active,
		       created_at, updated_at
		FROM search_configs WHERE id = $1`, id).
		Scan(&c.ID, &c.SourceID, &c.Country, &c.DealType, &c.PropertyType,
			&attrsRaw, &c.MinAreaSqM, &c.MaxAreaSqM, &c.MinPriceMinor, &c.MaxPriceMinor,
			&c.Currency, &c.Active, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, httpError(http.StatusNotFound, "конфигурация %d не найдена", id))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := json.Unmarshal(attrsRaw, &c.FilterAttributes); err != nil {
		c.FilterAttributes = map[string]any{}
	}
	writeJSON(w, http.StatusOK, c)
}

// POST /api/search-configs
func (s *Server) handleCreateSearchConfig(w http.ResponseWriter, r *http.Request) {
	var in searchConfigIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.validateSearchConfig(r.Context(), &in); err != nil {
		writeErr(w, err)
		return
	}
	attrs, _ := json.Marshal(in.FilterAttributes)

	var id int64
	err := s.Pool.QueryRow(r.Context(), `
		INSERT INTO search_configs
			(source_id, country, deal_type, property_type, filter_attributes,
			 min_area_sqm, max_area_sqm, min_price_minor, max_price_minor, currency, active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, COALESCE($11, true))
		RETURNING id`,
		in.SourceID, in.Country, in.DealType, in.PropertyType, attrs,
		in.MinAreaSqM, in.MaxAreaSqM, in.MinPriceMinor, in.MaxPriceMinor,
		in.Currency, in.Active).Scan(&id)
	if err != nil {
		writeErr(w, httpError(http.StatusBadRequest, "создание конфигурации: %v", err))
		return
	}
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"id":%d}`, id)
}

// PUT /api/search-configs/{id} — полная замена полей.
func (s *Server) handleUpdateSearchConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, httpError(http.StatusBadRequest, "id: не целое"))
		return
	}
	var in searchConfigIn
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.Active == nil {
		writeErr(w, httpError(http.StatusBadRequest, "active: обязательное поле"))
		return
	}
	if err := s.validateSearchConfig(ctx, &in); err != nil {
		writeErr(w, err)
		return
	}
	attrs, _ := json.Marshal(in.FilterAttributes)
	tag, err := s.Pool.Exec(ctx, `
		UPDATE search_configs SET
			source_id = $2, country = $3, deal_type = $4, property_type = $5,
			filter_attributes = $6,
			min_area_sqm = $7, max_area_sqm = $8, min_price_minor = $9, max_price_minor = $10,
			currency = $11, active = $12, updated_at = now()
		WHERE id = $1`,
		id, in.SourceID, in.Country, in.DealType, in.PropertyType, attrs,
		in.MinAreaSqM, in.MaxAreaSqM, in.MinPriceMinor, in.MaxPriceMinor,
		in.Currency, *in.Active)
	if err != nil {
		writeErr(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, httpError(http.StatusNotFound, "конфигурация %d не найдена", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/search-configs/{id} — мягкое удаление (active = false):
// история сканов ссылается на конфигурацию, жёсткое удаление её разрывает.
func (s *Server) handleDeleteSearchConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, httpError(http.StatusBadRequest, "id: не целое"))
		return
	}
	tag, err := s.Pool.Exec(ctx,
		"UPDATE search_configs SET active = false, updated_at = now() WHERE id = $1", id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, httpError(http.StatusNotFound, "конфигурация %d не найдена", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

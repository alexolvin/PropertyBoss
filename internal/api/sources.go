package api

import (
	"encoding/json"
	"net/http"
)

type sourceOut struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Domain    string   `json:"domain"`
	Country   string   `json:"country"`
	DealTypes []string `json:"deal_types"`
	Kind      string   `json:"kind"`
	State     string   `json:"state"`
}

// GET /api/sources — только чтение.
// Регистрация источников — этап 3 после проверки доступа (ТЗ §13).
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Pool.Query(r.Context(),
		"SELECT id, name, domain, country, deal_types, kind, state FROM sources ORDER BY id")
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []sourceOut{}
	for rows.Next() {
		var sr sourceOut
		if err := rows.Scan(&sr.ID, &sr.Name, &sr.Domain, &sr.Country, &sr.DealTypes, &sr.Kind, &sr.State); err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, sr)
	}
	writeJSON(w, http.StatusOK, out)
}

type attributeOut struct {
	Key            string   `json:"key"`
	DataType       string   `json:"data_type"`
	AllowedValues  []string `json:"allowed_values,omitempty"`
	UsedInPricing  bool     `json:"used_in_pricing"`
	LabelRu        string   `json:"label_ru"`
	LabelEn        string   `json:"label_en"`
	SourceEvidence string   `json:"source_evidence"`
}

// GET /api/attribute-registry?country=CZ — реестр для построения фильтров в UI.
func (s *Server) handleListAttributeRegistry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := `SELECT key, data_type, allowed_values, used_in_pricing, label_ru, label_en, source_evidence
		FROM attribute_registry`
	var args []any
	if c := r.URL.Query().Get("country"); c != "" {
		q += " WHERE country = $1"
		args = append(args, c)
	}
	q += " ORDER BY key"
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []attributeOut{}
	for rows.Next() {
		var a attributeOut
		var allowedRaw []byte
		if err := rows.Scan(&a.Key, &a.DataType, &allowedRaw, &a.UsedInPricing, &a.LabelRu, &a.LabelEn, &a.SourceEvidence); err != nil {
			writeErr(w, err)
			return
		}
		if allowedRaw != nil {
			_ = json.Unmarshal(allowedRaw, &a.AllowedValues)
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

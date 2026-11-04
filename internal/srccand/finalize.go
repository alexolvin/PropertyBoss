package srccand

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ArtifactClass — класс, выведенный из завершённой пробы (не из догадки).
type ArtifactClass struct {
	Domain string
	Class  string
	Reason string
}

// ClassifyArtifact — pending после завершённого P1–P12: класс по сырому probe.json.
// Двухпрогонные наблюдения (1/2) не трогаем.
func ClassifyArtifact(probeJSON []byte, homeHTML string) (class, reason string, ok bool) {
	var p struct {
		Class         string `json:"class"`
		ClassReason   string `json:"class_reason"`
		ObservedClass string `json:"observed_class"`
		Channel       *string `json:"channel"`
		FieldSample   json.RawMessage `json:"field_sample"`
		Steps         []struct {
			ID, Status, Detail string
		} `json:"steps"`
		Domain string `json:"domain"`
	}
	if err := json.Unmarshal(probeJSON, &p); err != nil {
		return "", "", false
	}
	if strings.Contains(p.ClassReason, "наблюдение 1/2") {
		return "", "", false
	}
	if p.Class != "pending" && p.Class != "" {
		return p.Class, p.ClassReason, true
	}

	low := strings.ToLower(homeHTML)
	if strings.Count(low, "ceskereality") >= 5 {
		return "mirror", "P11: в HTML ≥5 вхождений ceskereality — региональный сервер сети ceskereality.cz (один оператор, Vue 3.2)", true
	}
	if strings.Contains(low, "re/max") || strings.Contains(low, "remax") {
		return "agency_site", "бренд RE/MAX — сайт агентства, вне области", true
	}
	if strings.Contains(low, "m&m reality") || strings.Contains(low, "mm reality") || strings.Contains(low, "mmreality") {
		return "agency_site", "бренд M&M Reality — сайт агентства, вне области", true
	}
	if strings.Contains(low, "century 21") || strings.Contains(low, "century21") {
		return "agency_site", "бренд Century 21 — сайт агентства, вне области", true
	}
	if p.Domain == "bezrealitky.sk" || strings.HasSuffix(p.Domain, ".sk") {
		return "excluded_no_data", "площадка не ЧР (SK) — в реестр коннекторов CZ не входит", true
	}
	if p.Domain == "bezrealitky.com" {
		return "excluded_no_data", "международная витрина без корпуса ЧР как отдельной базы", true
	}

	var wrap struct {
		Coverage struct {
			N           int `json:"n"`
			ID          int `json:"id"`
			URL         int `json:"url"`
			PriceOrFlag int `json:"price_or_flag"`
			DealType    int `json:"deal_type"`
			PropertyType int `json:"property_type"`
		} `json:"coverage"`
	}
	_ = json.Unmarshal(p.FieldSample, &wrap)
	c := wrap.Coverage
	p6 := false
	for _, s := range p.Steps {
		if s.ID == "P6" && s.Status == "pass" {
			p6 = true
		}
	}
	if c.N >= 30 && c.ID == c.N && c.URL == c.N && c.PriceOrFlag == c.N && c.DealType == c.N && c.PropertyType == c.N {
		ch := "html"
		if p.Channel != nil {
			ch = *p.Channel
		}
		reason = fmt.Sprintf("P5=%s P7 n=%d 100%% id/url/price/deal/type", ch, c.N)
		if p6 {
			reason += " P6 схема есть"
		} else {
			reason += " P6: схема = листинг+детали (выборка 30), отдельного счётчика нет"
		}
		return "direct", reason, true
	}
	if c.N == 0 {
		return "excluded_no_data", "проба P1–P12 завершена: деталей объявлений не извлечено (P7 n=0)", true
	}
	return "excluded_no_data", fmt.Sprintf("проба завершена, условие direct не выполнено: P7 n=%d id=%d/%d price=%d/%d deal=%d/%d type=%d/%d",
		c.N, c.ID, c.N, c.PriceOrFlag, c.N, c.DealType, c.N, c.PropertyType, c.N), true
}

func loadHome(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "bodies", "01-home.html"))
	if err != nil {
		return ""
	}
	return string(b)
}

func latestProbeDir(root, domain string) string {
	base := filepath.Join(root, domain)
	ents, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	var best string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(base, e.Name())
		if _, err := os.Stat(filepath.Join(p, "probe.json")); err != nil {
			continue
		}
		if e.Name() > best {
			best = e.Name()
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(base, best)
}

// FinalizePending — переклассификация pending по последнему probe.json.
func FinalizePending(ctx context.Context, pool *pgxpool.Pool, root string) (n int, err error) {
	if root == "" {
		root = "artifacts/probes"
	}
	list, err := List(ctx, pool)
	if err != nil {
		return 0, err
	}
	for _, c := range list {
		if c.Domain == "bezrealitky.sk" || strings.HasSuffix(c.Domain, ".sk") {
			if c.Class == "direct" || c.Class == "pending" {
				ev := ""
				if c.EvidencePath != nil {
					ev = *c.EvidencePath
				}
				if err := SaveUpdate(ctx, pool, Update{
					Domain: c.Domain, Class: "excluded_no_data",
					Reason: "площадка не ЧР (SK) — коннектор CZ не строится",
					Evidence: ev, RunID: "finalize-sk",
					Channel: c.Channel, RobotsOK: c.RobotsOK,
				}); err != nil {
					return n, err
				}
				n++
			}
			continue
		}
		if c.Class != "pending" {
			continue
		}
		if c.ClassReason != nil && strings.Contains(*c.ClassReason, "наблюдение 1/2") {
			continue
		}
		dir := latestProbeDir(root, c.Domain)
		if dir == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "probe.json"))
		if err != nil {
			return n, err
		}
		class, reason, ok := ClassifyArtifact(raw, loadHome(dir))
		if !ok || class == "" || class == "pending" {
			continue
		}
		if err := SaveUpdate(ctx, pool, Update{
			Domain: c.Domain, Class: class, Reason: reason, Evidence: dir, RunID: "finalize-" + filepath.Base(dir),
			Channel: c.Channel, RobotsOK: c.RobotsOK, TosURL: c.TosURL, TosExcerpt: c.TosExcerpt, MirrorOf: c.MirrorOf,
			ReportedTotals: c.ReportedTotals, FieldSample: c.FieldSample, DelistSig: c.DelistSig,
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

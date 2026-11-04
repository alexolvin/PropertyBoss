package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"propertyboss/internal/robots"
)

// Config — параметры одного прогона pb probe.
type Config struct {
	Domain string
	UA     string
	Root   string // каталог артефактов (artifacts/probes)
	Prev   *Prev
	Host   string // не записывается в probe.json: имя машины остаётся локальным
}

// Step — P1–P12.
type Step struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"` // pass | fail | skip | not_measured
	Detail   string   `json:"detail"`
	Evidence []string `json:"evidence,omitempty"`
}

// Result — probe.json.
type Result struct {
	Domain         string          `json:"domain"`
	RunID          string          `json:"run_id"`
	Host           string          `json:"host,omitempty"`
	UA             string          `json:"ua"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     time.Time       `json:"finished_at"`
	Requests       int             `json:"requests"`
	Class          string          `json:"class"`
	ClassReason    string          `json:"class_reason"`
	ObservedClass  string          `json:"observed_class"`
	Channel        *string         `json:"channel"`
	RobotsOK       *bool           `json:"robots_ok"`
	CrawlDelay     *float64        `json:"crawl_delay_sec,omitempty"`
	Origin         string          `json:"origin"`
	TosURL         *string         `json:"tos_url"`
	TosExcerpt     *string         `json:"tos_excerpt"`
	MirrorOf       *string         `json:"mirror_of"`
	ReportedTotals json.RawMessage `json:"reported_totals"`
	FieldSample    json.RawMessage `json:"field_sample"`
	DelistSig      json.RawMessage `json:"delist_signature"`
	Outbound       []string        `json:"outbound_domains"`
	Steps          []Step          `json:"steps"`
	EvidencePath   string          `json:"evidence_path"`
	Stopped        bool            `json:"circuit_breaker"`
}

type runner struct {
	cfg   Config
	cli   *Client
	dir   string
	jsonl *os.File
	nBody int
	rob   *robots.File
	ua    string
	res   *Result
}

// Run — протокол пробы P1–P12, бюджет ≤ 80, A4–A8.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	domain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cfg.Domain), "www."))
	if domain == "" || !strings.Contains(domain, ".") {
		return nil, fmt.Errorf("probe: некорректный домен %q", cfg.Domain)
	}
	ua := cfg.UA
	if ua == "" {
		ua = DefaultUA
	}
	// Имя машины в probe.json не пишется: файл легко попадает в чужой архив.
	root := cfg.Root
	if root == "" {
		root = "artifacts/probes"
	}
	runID := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(root, domain, runID)
	if err := os.MkdirAll(filepath.Join(dir, "bodies"), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cli := NewClient(ua, 30*time.Second)
	r := &runner{
		cfg: cfg, cli: cli, dir: dir, jsonl: f, ua: ua,
		res: &Result{
			Domain:    domain,
			RunID:     runID,
			UA:        ua,
			StartedAt: time.Now().UTC(),
			Class:     "pending",
		},
	}
	r.res.EvidencePath = dir

	observed, reason := r.protocol(ctx, domain)
	r.res.ObservedClass = observed
	class, creason := DecideClass(domain, observed, reason, cfg.Prev)
	r.res.Class = class
	r.res.ClassReason = creason
	r.res.Requests = cli.Requests()
	r.res.Stopped = cli.Stopped()
	r.res.FinishedAt = time.Now().UTC()

	raw, err := json.MarshalIndent(r.res, "", "  ")
	if err != nil {
		return r.res, err
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.json"), raw, 0o644); err != nil {
		return r.res, err
	}
	return r.res, nil
}

func (r *runner) step(id, status, detail string, ev ...string) {
	r.res.Steps = append(r.res.Steps, Step{ID: id, Status: status, Detail: detail, Evidence: ev})
}

func (r *runner) fetch(ctx context.Context, rawURL, name string) (body []byte, rec Rec, kind Kind, err error) {
	if r.rob != nil && !r.rob.Allowed(r.ua, rawURL) {
		rec = Rec{Method: "GET", URL: rawURL, Err: "robots.txt disallow"}
		r.logRec(rec)
		return nil, rec, KindNone, fmt.Errorf("robots.txt запрещает %s", rawURL)
	}
	body, rec, kind, err = r.cli.Get(ctx, rawURL)
	r.logRec(rec)
	if err == nil && len(body) > 0 {
		r.saveBody(name, rec, body)
	}
	return body, rec, kind, err
}

func (r *runner) logRec(rec Rec) {
	b, _ := json.Marshal(rec)
	_, _ = r.jsonl.Write(append(b, '\n'))
}

func (r *runner) saveBody(name string, rec Rec, body []byte) {
	r.nBody++
	ext := ".bin"
	ct := strings.ToLower(rec.Headers["content-type"])
	switch {
	case strings.Contains(ct, "json"):
		ext = ".json"
	case strings.Contains(ct, "html"):
		ext = ".html"
	case strings.Contains(ct, "xml") || strings.Contains(ct, "text/plain"):
		ext = ".txt"
	}
	safe := regexp.MustCompile(`[^a-zA-Z0-9._-]+`).ReplaceAllString(name, "_")
	if len(safe) > 80 {
		safe = safe[:80]
	}
	path := filepath.Join(r.dir, "bodies", fmt.Sprintf("%02d-%s%s", r.nBody, safe, ext))
	_ = os.WriteFile(path, body, 0o644)
}

func (r *runner) protocol(ctx context.Context, domain string) (observed, reason string) {
	// P1 главная
	var home []byte
	var homeRec Rec
	var homeKind Kind
	var origin string
	candidates := []string{"https://www." + domain + "/", "https://" + domain + "/"}
	var lastErr error
	for _, u := range candidates {
		if r.cli.Stopped() {
			break
		}
		b, rec, kind, err := r.cli.Get(ctx, u)
		r.logRec(rec)
		if err != nil {
			lastErr = err
			continue
		}
		r.saveBody("home", rec, b)
		home, homeRec, homeKind = b, rec, kind
		origin = originOf(rec.FinalURL)
		break
	}
	if home == nil {
		r.step("P1", "fail", fmt.Sprintf("главная недоступна: %v", lastErr))
		return "dead", fmt.Sprintf("главная не ответила: %v", lastErr)
	}
	if homeKind == KindChallenge {
		r.step("P1", "fail", "челлендж на главной", homeRec.FinalURL)
		r.step("P4", "fail", string(homeKind))
		return "excluded_challenge", "P4 челлендж на главной, status=" + itoa(homeRec.Status)
	}
	if homeKind == KindCaptcha {
		r.step("P1", "pass", "главная открылась, но капча-стена")
		r.step("P4", "fail", string(homeKind))
		return "excluded_captcha", "P4 CAPTCHA-стена на главной"
	}
	if homeKind == KindLogin {
		r.step("P1", "pass", homeRec.FinalURL)
		r.step("P3", "fail", "форма входа без листинга")
		return "excluded_login", "P3 страница входа вместо данных"
	}
	finalHost := hostOf(homeRec.FinalURL)
	r.res.Origin = origin
	if looksParking(string(home)) {
		r.step("P1", "fail", "парковка/заглушка", homeRec.FinalURL)
		return "dead", "парковочная заглушка"
	}
	r.step("P1", "pass", fmt.Sprintf("status=%d final=%s", homeRec.Status, homeRec.FinalURL), homeRec.FinalURL)

	// P2 robots.txt
	robURL := origin + "/robots.txt"
	rb, rec, _, err := r.cli.Get(ctx, robURL)
	r.logRec(rec)
	robotsOK := true
	if err != nil {
		r.step("P2", "not_measured", fmt.Sprintf("robots.txt не загружен: %v — RFC: нет файла = разрешено", err))
	} else {
		r.saveBody("robots", rec, rb)
		if rec.Status == 200 && len(rb) > 0 {
			r.rob = robots.Parse(string(rb))
			r.res.CrawlDelay = r.rob.Delay(r.ua)
			// пути коннектора ещё не известны: корень и типичный листинг
			if !r.rob.Allowed(r.ua, origin+"/") {
				robotsOK = false
			}
			r.step("P2", "pass", fmt.Sprintf("robots разобран, sitemaps=%d, root_allowed=%v, crawl-delay=%v",
				len(r.rob.Sitemaps), robotsOK, r.res.CrawlDelay))
		} else {
			r.step("P2", "pass", fmt.Sprintf("robots.txt status=%d — нет правил = разрешено", rec.Status))
		}
	}
	r.res.RobotsOK = &robotsOK
	if !robotsOK {
		r.step("P4", "skip", "исключение по robots, защита не оценивалась как канал")
		return "excluded_robots", "P2 User-agent проекта: Disallow нужного пути (корень)"
	}

	if r.cli.Stopped() {
		return "excluded_blocked", "circuit breaker после 3× 403/429/челлендж"
	}

	html := string(home)
	if looksAuctionPage(html) && !reListingKW.MatchString(html) {
		return "auction", "P1 контент аукциона без каталога объявлений"
	}

	r.res.Outbound = harvestHosts(html, domain, true)

	// P12 ToS (раньше, пока есть бюджет; не определяет класс)
	r.doTos(ctx, domain, origin, html)

	// P3/P4 листинг
	hrefs := extractHrefs(homeRec.FinalURL, html)
	fromND := nextDataDetailURLs(origin, html, hrefs)
	hrefs = append(hrefs, fromND...)
	listURLs := pickListingURLs(domain, hrefs, 4)
	var listBody []byte
	var listRec Rec
	var listURL string
	for _, u := range listURLs {
		if r.cli.Remain() < 8 || r.cli.Stopped() {
			break
		}
		b, rec, kind, err := r.fetch(ctx, u, "listing")
		if err != nil {
			continue
		}
		if kind == KindChallenge {
			r.step("P4", "fail", "челлендж на листинге")
			return "excluded_challenge", "P4 челлендж на " + u
		}
		if kind == KindCaptcha {
			r.step("P4", "fail", "CAPTCHA-стена на листинге")
			return "excluded_captcha", "P4 CAPTCHA на " + u
		}
		if kind == KindLogin {
			r.step("P3", "fail", "логин на листинге")
			return "excluded_login", "P3 логин на " + u
		}
		if looksListing(strings.ToLower(string(b))) || scoreListingURL(u) > 0 {
			listBody, listRec, listURL = b, rec, rec.FinalURL
			break
		}
	}
	if listBody == nil {
		listBody, listRec, listURL = home, homeRec, homeRec.FinalURL
	}

	if looksAgencyPage(string(listBody)) && !hasMultiAdvertiser(string(listBody)) && !isBazos(domain) {
		// агентство без чужих рекламодателей — вне области
		if looksAgencyPage(html) {
			r.step("P3", "pass", "анонимный доступ есть")
			r.step("P4", "pass", "стены нет")
			r.step("P5", "skip", "agency_site — коннектор не строится")
			return "agency_site", "бренд агентства / «наше портфолио» без агрегации"
		}
	}

	if !looksListing(strings.ToLower(string(listBody))) && !reListingKW.MatchString(string(listBody)) {
		r.step("P3", "fail", "нет собственных объявлений на главной/листинге")
		r.step("P5", "fail", "канал данных не найден")
		return "excluded_no_data", "нет каталога объявлений (перелинковка/заглушка)"
	}
	r.step("P3", "pass", fmt.Sprintf("листинг без авторизации status=%d %s", listRec.Status, listURL))
	r.step("P4", "pass", "нет челленджа/капчи-стены/consent wall вместо данных")

	// P5 канал
	ch, chDetail := r.discoverChannel(ctx, domain, origin, homeRec.FinalURL, html, string(listBody), listURL)
	if ch != "" {
		r.res.Channel = &ch
		r.step("P5", "pass", chDetail)
	} else {
		r.step("P5", "fail", chDetail)
		return "pending", "P5 канал не определён: " + chDetail
	}

	// P9 фильтры — сырой артефакт формы
	r.doFilters(string(listBody), listURL)

	// P6 пагинация + P10 объём
	r.doVolume(ctx, string(listBody), listURL)

	// P7 поля (до 30 деталей, оставляем запас)
	r.doFields(ctx, domain, listURL, string(listBody))

	// P8 delist
	r.step("P8", "not_measured", "реального снятого объявления в пробе нет")
	r.res.DelistSig, _ = json.Marshal(map[string]any{"status": "not_measured", "reason": "нет известного снятого объявления"})

	// P11 зеркало — по редиректу на чужой домен
	if finalHost != "" && !sameSite("https://"+finalHost+"/", domain) && !strings.HasSuffix(finalHost, domain) {
		m := finalHost
		r.res.MirrorOf = &m
		r.step("P11", "pass", "редирект на чужой домен "+finalHost)
		return "mirror", "P1 финальный домен " + finalHost
	}
	r.step("P11", "pass", "признака зеркала в этом прогоне нет")

	if r.cli.Stopped() {
		return "excluded_blocked", "circuit breaker"
	}

	// условие direct
	if r.res.Channel == nil {
		return "pending", "канал пуст"
	}
	var cov FieldCoverage
	if len(r.res.FieldSample) > 0 {
		var wrap struct {
			Coverage FieldCoverage `json:"coverage"`
			Samples  []Sample      `json:"samples"`
		}
		_ = json.Unmarshal(r.res.FieldSample, &wrap)
		cov = wrap.Coverage
	}
	p6ok := false
	for _, s := range r.res.Steps {
		if s.ID == "P6" && s.Status == "pass" {
			p6ok = true
		}
	}
	if cov.N >= 30 && cov.ID == cov.N && cov.URL == cov.N && cov.PriceOrFlag == cov.N && cov.DealType == cov.N && cov.PropertyType == cov.N && p6ok {
		return "direct", fmt.Sprintf("P5=%s P6 схема есть P7 n=%d 100%% id/url/price/deal/type", *r.res.Channel, cov.N)
	}
	return "pending", fmt.Sprintf("не direct: P6=%v P7 n=%d id=%d/%d price=%d/%d deal=%d/%d type=%d/%d (нужно n≥30 и 100%% id,url,price|флаг,deal,type)",
		p6ok, cov.N, cov.ID, cov.N, cov.PriceOrFlag, cov.N, cov.DealType, cov.N, cov.PropertyType, cov.N)
}

func (r *runner) discoverChannel(ctx context.Context, domain, origin, homeURL, homeHTML, listHTML, listURL string) (ch, detail string) {
	// embedded JSON
	if nd := nextData(homeHTML); nd != "" {
		ch = "embedded_json"
		return ch, "P5 __NEXT_DATA__ на главной, байт=" + itoa(len(nd))
	}
	if nd := nextData(listHTML); nd != "" {
		ch = "embedded_json"
		return ch, "P5 __NEXT_DATA__ на листинге"
	}
	if hasNuxt(homeHTML) || hasNuxt(listHTML) {
		ch = "embedded_json"
		return ch, "P5 __NUXT__ в HTML"
	}

	// GraphQL / API hints from HTML, then a few same-site JS bundles
	gql := graphqlHints(homeHTML + listHTML)
	apis := apiHints(homeHTML + listHTML)
	scripts := extractScripts(homeURL, homeHTML)
	for i, s := range scripts {
		if i >= 3 || r.cli.Remain() < 12 || r.cli.Stopped() {
			break
		}
		if !sameSite(s, domain) {
			continue
		}
		if r.rob != nil && !r.rob.Allowed(r.ua, s) {
			continue
		}
		b, rec, _, err := r.cli.Get(ctx, s)
		r.logRec(rec)
		if err != nil {
			continue
		}
		r.saveBody("js", rec, b)
		gql = append(gql, graphqlHints(string(b))...)
		apis = append(apis, apiHints(string(b))...)
	}
	if len(gql) > 0 {
		// подтверждаем канал сырым ответом: GET на найденный путь, если абсолютный
		for _, g := range gql {
			u := g
			if strings.HasPrefix(g, "/") {
				u = origin + g
			}
			if !strings.HasPrefix(u, "http") {
				continue
			}
			if r.rob != nil && !r.rob.Allowed(r.ua, u) {
				continue
			}
			if r.cli.Remain() < 8 {
				break
			}
			b, rec, kind, err := r.cli.Get(ctx, u)
			r.logRec(rec)
			if err != nil || kind != KindNone {
				continue
			}
			r.saveBody("graphql", rec, b)
			if rec.Status >= 200 && rec.Status < 500 {
				ch = "api_graphql"
				return ch, "P5 GraphQL URL " + u + " status=" + itoa(rec.Status)
			}
		}
		return "html", "P5 GraphQL упоминается (" + strings.Join(gql, ", ") + "), сырого успешного ответа нет — канал html"
	}
	if len(apis) > 0 {
		for _, a := range apis {
			u := a
			if strings.HasPrefix(a, "/") {
				u = origin + a
			}
			if !strings.HasPrefix(u, "http") || !sameSite(u, domain) {
				continue
			}
			if r.rob != nil && !r.rob.Allowed(r.ua, u) {
				r.res.RobotsOK = boolPtr(false)
				continue
			}
			if r.cli.Remain() < 8 {
				break
			}
			b, rec, kind, err := r.cli.Get(ctx, u)
			r.logRec(rec)
			if err != nil || kind != KindNone {
				continue
			}
			r.saveBody("api", rec, b)
			ct := strings.ToLower(rec.Headers["content-type"])
			if rec.Status == 200 && (strings.Contains(ct, "json") || looksJSON(b)) {
				ch = "api_json"
				return ch, "P5 JSON API " + u
			}
		}
	}

	// sitemap
	var sm []string
	if r.rob != nil {
		sm = r.rob.Sitemaps
	}
	if len(sm) == 0 {
		sm = []string{origin + "/sitemap.xml"}
	}
	if r.cli.Remain() >= 10 && !r.cli.Stopped() {
		u := sm[0]
		if r.rob == nil || r.rob.Allowed(r.ua, u) {
			b, rec, _, err := r.cli.Get(ctx, u)
			r.logRec(rec)
			if err == nil && rec.Status == 200 && strings.Contains(string(b), "<url") {
				r.saveBody("sitemap", rec, b)
				ch = "sitemap_feed"
				return ch, "P5 sitemap " + u + " loc-теги есть"
			}
		}
	}

	if looksListing(strings.ToLower(listHTML)) || scoreListingURL(listURL) > 0 {
		ch = "html"
		return ch, "P5 HTML-листинг " + listURL
	}
	return "", "ни JSON/GraphQL, ни sitemap, ни HTML-листинга"
}

func (r *runner) doFilters(listHTML, listURL string) {
	if strings.Contains(strings.ToLower(listHTML), "<form") {
		r.step("P9", "pass", "на листинге есть <form> (фильтры)", listURL)
		return
	}
	r.step("P9", "not_measured", "формы фильтров в теле листинга нет")
}

func (r *runner) doVolume(ctx context.Context, listHTML, listURL string) {
	total := parseTotalHint(listHTML)
	p6 := "схема не измерена"
	st := "not_measured"
	if strings.Contains(listHTML, "page=") || strings.Contains(listHTML, "strana=") || strings.Contains(strings.ToLower(listHTML), "rel=\"next\"") {
		p6 = "пагинация по page/strana/rel=next видна в HTML"
		st = "pass"
	}
	if total != nil {
		p6 += fmt.Sprintf("; счётчик на странице=%d", *total)
		st = "pass"
	}
	r.step("P6", st, p6)

	tot := map[string]any{"list_url": listURL}
	if total != nil {
		tot["list_counter"] = *total
		tot["raw"] = fmt.Sprintf("counter=%d url=%s", *total, listURL)
	}
	// sitemap count if channel is sitemap or robots has sitemap
	if r.rob != nil && len(r.rob.Sitemaps) > 0 && r.cli.Remain() >= 6 {
		u := r.rob.Sitemaps[0]
		if r.rob.Allowed(r.ua, u) {
			b, rec, _, err := r.cli.Get(ctx, u)
			r.logRec(rec)
			if err == nil && rec.Status == 200 {
				r.saveBody("sitemap-p10", rec, b)
				n := strings.Count(string(b), "<loc>")
				tot["sitemap_loc"] = n
				tot["sitemap_url"] = u
			}
		}
	}
	r.res.ReportedTotals, _ = json.Marshal(tot)
	raw, _ := json.Marshal(tot)
	r.step("P10", "pass", "reported_totals="+string(raw))
}

func (r *runner) doFields(ctx context.Context, domain, listURL, listHTML string) {
	hrefs := extractHrefs(listURL, listHTML)
	hrefs = append(hrefs, nextDataDetailURLs(r.res.Origin, listHTML, hrefs)...)
	dets := pickDetailURLs(domain, listURL, hrefs, 50)
	// если деталей мало — одна следующая страница листинга (пагинация P6/P7)
	if len(dets) < 30 && r.cli.Remain() >= 10 && !r.cli.Stopped() {
		next := nextPageURL(listURL, listHTML)
		if next != "" && sameSite(next, domain) {
			b, rec, kind, err := r.fetch(ctx, next, "listing-p2")
			if err == nil && kind == KindNone && rec.Status == 200 {
				more := pickDetailURLs(domain, rec.FinalURL, extractHrefs(rec.FinalURL, string(b)), 50)
				seen := map[string]bool{}
				for _, u := range dets {
					seen[u] = true
				}
				for _, u := range more {
					if !seen[u] {
						dets = append(dets, u)
					}
				}
			}
		}
	}
	var samples []Sample
	for _, u := range dets {
		if len(samples) >= 30 || r.cli.Remain() < 4 || r.cli.Stopped() {
			break
		}
		b, rec, kind, err := r.fetch(ctx, u, "detail")
		if err != nil || kind != KindNone {
			continue
		}
		if rec.Status != 200 {
			continue
		}
		samples = append(samples, parseSample(rec.FinalURL, b))
	}
	cov := cover(samples)
	wrap := map[string]any{"coverage": cov, "samples": samples, "categories_note": "категории — по URL листинга; 3 категории не гарантированы"}
	r.res.FieldSample, _ = json.Marshal(wrap)
	st := "not_measured"
	if cov.N >= 30 {
		st = "pass"
	} else if cov.N > 0 {
		st = "fail"
	}
	r.step("P7", st, fmt.Sprintf("n=%d id=%d url=%d price_or_flag=%d deal=%d type=%d area=%d loc=%d",
		cov.N, cov.ID, cov.URL, cov.PriceOrFlag, cov.DealType, cov.PropertyType, cov.Area, cov.Location))
}

func (r *runner) doTos(ctx context.Context, domain, origin, homeHTML string) {
	links := tosLinks(domain, origin+"/", homeHTML)
	if len(links) == 0 {
		r.step("P12", "not_measured", "ссылка на ToS в HTML не найдена; tos_excerpt=NULL")
		return
	}
	u := links[0]
	if r.rob != nil && !r.rob.Allowed(r.ua, u) {
		r.step("P12", "not_measured", "ToS URL найден, но robots запрещает: "+u)
		r.res.TosURL = &u
		return
	}
	if r.cli.Remain() < 6 {
		r.step("P12", "not_measured", "бюджет не оставил запрос на ToS")
		r.res.TosURL = &u
		return
	}
	b, rec, _, err := r.fetch(ctx, u, "tos")
	r.res.TosURL = &u
	if err != nil {
		r.step("P12", "not_measured", err.Error())
		return
	}
	ex := tosExcerpt(string(b))
	if ex == "" {
		r.step("P12", "pass", "ToS загружен, выдержки о запрете автомата нет")
		return
	}
	r.res.TosExcerpt = &ex
	r.step("P12", "pass", "выдержка сохранена", rec.SHA256)
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func boolPtr(v bool) *bool { return &v }

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func hasMultiAdvertiser(html string) bool {
	// грубый признак агрегатора: много разных «RK»/агентств в выдаче
	n := strings.Count(strings.ToLower(html), "realitn") + strings.Count(strings.ToLower(html), "rk ")
	return n >= 3
}

// SHA256File — сверка фикстуры (S6).
func SHA256File(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

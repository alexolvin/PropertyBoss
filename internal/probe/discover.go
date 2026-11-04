package probe

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	reHref     = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+)["']`)
	reSrc      = regexp.MustCompile(`(?i)<script[^>]+src\s*=\s*["']([^"']+)["']`)
	reNextData = regexp.MustCompile(`(?is)<script[^>]+id=["']__NEXT_DATA__["'][^>]*>(.*?)</script>`)
	reJSONLD   = regexp.MustCompile(`(?is)<script[^>]+type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
	reGraphQL  = regexp.MustCompile(`(?i)(["'/][a-z0-9_./-]*graphql[a-z0-9_./-]*|["']/gql["'])`)
	reAPIPath  = regexp.MustCompile(`(?i)["'](https?://[^"']+/api/[^"']+|/[a-z0-9_./-]*api/[a-z0-9_./?-]*)["']`)
	reListingKW = regexp.MustCompile(`(?i)(nemovit|realit|inzer[aá]t|prodej|pron[aá]j|byt[yů]?|d[uů]m|domy|pozemek|chalup)`)
	rePortalKW  = regexp.MustCompile(`(?i)(realit|reality|nemovit|bydl|byt|dum|domy|pronaj|inzer)`)
	reTosPath   = regexp.MustCompile(`(?i)(obchodn[ií].*podm|podm[ií]nky|terms|terms-of|vop\b|ochrana.?udaj|privacy)`)
	reSkipHost  = regexp.MustCompile(`(?i)(google|gstatic|googleapis|gstatic|facebook|fbcdn|instagram|youtube|twitter|linkedin|cloudflare|schema\.org|w3\.org|apple\.com|microsoft|doubleclick|googletagmanager|hotjar|cookiebot|onetrust|recaptcha)`)
	rePark      = regexp.MustCompile(`(?i)(domain is for sale|this domain|parked free|godaddy|sedoparking|coming soon|web not configured|zakoupen[ií] domény)`)
	reAuction   = regexp.MustCompile(`(?i)(dražb|aukc[ei]|auction)`)
	reAgency    = regexp.MustCompile(`(?i)(re/?max|century\s*21|m\s*&\s*m\s*reality|mm reality|naše (?:nabídky|zakázky)|realitní kancelář)`)
)

func resolveURL(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") || href == "#" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
}

func sameSite(got, domain string) bool {
	g := hostOf(got)
	if g == "" {
		g = strings.ToLower(strings.TrimPrefix(got, "www."))
	}
	d := strings.ToLower(strings.TrimPrefix(domain, "www."))
	return g == d || strings.HasSuffix(g, "."+d)
}

func extractHrefs(base, html string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reHref.FindAllStringSubmatch(html, -1) {
		u := resolveURL(base, m[1])
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

func extractScripts(base, html string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reSrc.FindAllStringSubmatch(html, -1) {
		u := resolveURL(base, m[1])
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

func nextData(html string) string {
	m := reNextData.FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func jsonLD(html string) []string {
	var out []string
	for _, m := range reJSONLD.FindAllStringSubmatch(html, 8) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

func hasNuxt(html string) bool {
	return strings.Contains(html, "__NUXT__")
}

func graphqlHints(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reGraphQL.FindAllStringSubmatch(body, 20) {
		s := strings.Trim(m[1], `"'`)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func apiHints(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reAPIPath.FindAllStringSubmatch(body, 30) {
		s := strings.Trim(m[1], `"'`)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func looksParking(html string) bool {
	s := strings.ToLower(html)
	if len(html) < 800 && rePark.MatchString(s) {
		return true
	}
	return rePark.MatchString(s) && !reListingKW.MatchString(s)
}

func looksAuctionPage(html string) bool {
	return reAuction.MatchString(html) && !reListingKW.MatchString(html)
}

func looksAgencyPage(html string) bool {
	return reAgency.MatchString(html)
}

func isAssetURL(u string) bool {
	s := strings.ToLower(u)
	for _, e := range []string{
		".css", ".js", ".jpg", ".jpeg", ".png", ".gif", ".webp", ".jfif", ".svg",
		".woff", ".ico", "/_next/static", "/media/", "/img/", "/static/",
		"srcset", "fonts.",
	} {
		if strings.Contains(s, e) {
			return true
		}
	}
	return false
}

var (
	reDetailURI = regexp.MustCompile(`(?i)(?:/inzerat/\d+|/\d+-nabidka-|/nemovitosti-byty-domy/\d+|/\d{5,}(?:/|\.php|-|$))`)
	reCatalog   = regexp.MustCompile(`(?i)(/vypis/|/hledani|/hledání|/prodam/|/pronajmu/|/inzeraty/|/estates|/listing)`)
)

func scoreListingURL(u string) int {
	if isAssetURL(u) {
		return 0
	}
	s := strings.ToLower(u)
	if strings.Contains(s, "/blog") || strings.Contains(s, "/centrum-sluzeb") || strings.Contains(s, "/cenik") {
		return 0
	}
	if reDetailURI.MatchString(s) {
		return 0 // деталь, не каталог
	}
	n := 0
	if reCatalog.MatchString(s) {
		n += 5
	}
	for _, k := range []string{"nemovit", "realit", "prodej", "pronaj", "byt", "dum", "dům", "pozemek", "inzer"} {
		if strings.Contains(s, k) {
			n++
		}
	}
	return n
}

func pickListingURLs(domain string, hrefs []string, limit int) []string {
	type sc struct {
		u string
		n int
	}
	var cand []sc
	seen := map[string]bool{}
	for _, u := range hrefs {
		if !sameSite(u, domain) || seen[u] {
			continue
		}
		n := scoreListingURL(u)
		if n == 0 {
			continue
		}
		seen[u] = true
		cand = append(cand, sc{u, n})
	}
	// простой отбор: больший счёт, затем стабильный порядок
	out := []string{}
	for len(out) < limit && len(cand) > 0 {
		best := 0
		for i := 1; i < len(cand); i++ {
			if cand[i].n > cand[best].n {
				best = i
			}
		}
		out = append(out, cand[best].u)
		cand = append(cand[:best], cand[best+1:]...)
	}
	return out
}

func pickDetailURLs(domain, listURL string, hrefs []string, limit int) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range hrefs {
		if !sameSite(u, domain) || seen[u] || isAssetURL(u) {
			continue
		}
		s := strings.ToLower(u)
		if s == strings.ToLower(listURL) {
			continue
		}
		if !reDetailURI.MatchString(s) {
			continue
		}
		// без фрагмента/лишних query
		if i := strings.IndexByte(u, '#'); i >= 0 {
			u = u[:i]
		}
		seen[u] = true
		out = append(out, u)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// nextDataDetailURLs — uri объявлений из __NEXT_DATA__ (без выдуманных путей:
// собирается шаблон /nemovitosti-byty-domy/{uri} только если такой путь уже
// встречался в href страницы; иначе возвращаем сырой uri как относительный,
// если он начинается с /).
func nextDataDetailURLs(origin, html string, knownHrefs []string) []string {
	nd := nextData(html)
	if nd == "" {
		return nil
	}
	reURI := regexp.MustCompile(`"uri"\s*:\s*"([^"]+nabidka-[^"]+)"`)
	prefix := ""
	for _, h := range knownHrefs {
		if i := strings.Index(h, "/nemovitosti-byty-domy/"); i >= 0 {
			prefix = h[:i] + "/nemovitosti-byty-domy/"
			break
		}
		if i := strings.Index(h, "/inzerat/"); i >= 0 {
			prefix = h[:i] + "/inzerat/"
			break
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range reURI.FindAllStringSubmatch(nd, 80) {
		uri := m[1]
		var u string
		if strings.HasPrefix(uri, "/") {
			u = origin + uri
		} else if prefix != "" {
			u = prefix + uri
		} else {
			continue
		}
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

func tosLinks(domain, base, html string) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range extractHrefs(base, html) {
		if !sameSite(u, domain) || seen[u] {
			continue
		}
		if reTosPath.MatchString(u) {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// HarvestAll — D2: все домены со страницы-перечня (без CDN/соцсетей).
func HarvestAll(html, fromDomain string) []string {
	return harvestHosts(html, fromDomain, false)
}

func harvestHosts(html, fromDomain string, keywordOnly bool) []string {
	seen := map[string]bool{}
	var out []string
	from := strings.ToLower(strings.TrimPrefix(fromDomain, "www."))
	for _, u := range extractHrefs("https://"+fromDomain+"/", html) {
		h := hostOf(u)
		if h == "" || h == from || strings.HasSuffix(h, "."+from) {
			continue
		}
		if reSkipHost.MatchString(h) {
			continue
		}
		if strings.Contains(h, "jobs.") || strings.Contains(h, "cache.") ||
			strings.HasPrefix(h, "img-") || strings.HasPrefix(h, "cdn.") ||
			strings.HasPrefix(h, "static.") || strings.HasPrefix(h, "api.") {
			continue
		}
		if !strings.Contains(h, ".") {
			continue
		}
		if keywordOnly && !rePortalKW.MatchString(h) {
			// D1: имя домена или якорь. Якорь — текст рядом не разбираем
			// надёжно; без совпадения в домене пропускаем.
			continue
		}
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

func nextPageURL(listURL, html string) string {
	low := strings.ToLower(html)
	// rel="next"
	reNext := regexp.MustCompile(`(?i)<a[^>]+rel=["']next["'][^>]*href=["']([^"']+)["']`)
	if m := reNext.FindStringSubmatch(html); m != nil {
		return resolveURL(listURL, m[1])
	}
	reNext2 := regexp.MustCompile(`(?i)<a[^>]+href=["']([^"']+)["'][^>]*rel=["']next["']`)
	if m := reNext2.FindStringSubmatch(html); m != nil {
		return resolveURL(listURL, m[1])
	}
	u, err := url.Parse(listURL)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, k := range []string{"page", "strana", "p"} {
		if q.Get(k) != "" || strings.Contains(low, k+"=") {
			cur := 1
			if v := q.Get(k); v != "" {
				fmtS := v
				var n int
				fmt.Sscanf(fmtS, "%d", &n)
				if n > 0 {
					cur = n
				}
			}
			q.Set(k, fmt.Sprintf("%d", cur+1))
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return ""
}

func stripTags(s string) string {
	re := regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)
	s = re.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return strings.Join(strings.Fields(s), " ")
}

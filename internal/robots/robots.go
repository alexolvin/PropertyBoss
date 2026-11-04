// Package robots — разбор robots.txt по RFC 9309 для UA проекта (ТЗ-SRC-CZ-1 A4).
package robots

import (
	"bufio"
	"net/url"
	"strconv"
	"strings"
)

// File — разобранный robots.txt.
type File struct {
	Sitemaps    []string
	CrawlDelay  *float64 // секунды; nil — не задан для выбранного UA
	groups      []group
}

type group struct {
	agents []string // lower-case; "*" — все
	rules  []rule
	delay  *float64
}

type rule struct {
	allow bool
	pat   string // исходный путь-шаблон
}

// Parse — разбор тела robots.txt. Неизвестные директивы пропускаются.
func Parse(body string) *File {
	f := &File{}
	var cur *group
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		key, val, ok := splitField(line)
		if !ok {
			continue
		}
		switch key {
		case "user-agent":
			ua := strings.ToLower(strings.TrimSpace(val))
			if ua == "" {
				continue
			}
			// Новая группа, если текущая уже имеет правила (RFC: consecutive
			// User-agent lines share one group until Allow/Disallow).
			if cur == nil || len(cur.rules) > 0 || cur.delay != nil {
				g := group{}
				f.groups = append(f.groups, g)
				cur = &f.groups[len(f.groups)-1]
			}
			cur.agents = append(cur.agents, ua)
		case "allow":
			if cur != nil {
				cur.rules = append(cur.rules, rule{allow: true, pat: val})
			}
		case "disallow":
			if cur != nil {
				cur.rules = append(cur.rules, rule{allow: false, pat: val})
			}
		case "crawl-delay":
			if cur != nil {
				if d, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil && d >= 0 {
					cur.delay = &d
				}
			}
		case "sitemap":
			if u := strings.TrimSpace(val); u != "" {
				f.Sitemaps = append(f.Sitemaps, u)
			}
		}
	}
	return f
}

func splitField(line string) (key, val string, ok bool) {
	i := strings.Index(line, ":")
	if i <= 0 {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(line[:i])), strings.TrimSpace(line[i+1:]), true
}

// Allowed — путь (path+query) разрешён для ua (полная строка User-Agent).
// Пустой Disallow = разрешить всё в этой директиве. Нет правил = разрешено.
func (f *File) Allowed(ua, rawURL string) bool {
	path := pathOf(rawURL)
	g := f.matchingGroup(ua)
	if g == nil || len(g.rules) == 0 {
		return true
	}
	var best *rule
	bestLen := -1
	for i := range g.rules {
		r := &g.rules[i]
		if r.pat == "" {
			// Disallow:  → разрешить; Allow:  → разрешить
			continue
		}
		n := matchLen(r.pat, path)
		if n < 0 {
			continue
		}
		if n > bestLen || (n == bestLen && best != nil && r.allow && !best.allow) {
			best = r
			bestLen = n
		}
	}
	if best == nil {
		return true
	}
	return best.allow
}

// Delay — Crawl-delay выбранной группы, секунды.
func (f *File) Delay(ua string) *float64 {
	if g := f.matchingGroup(ua); g != nil {
		return g.delay
	}
	return nil
}

func (f *File) matchingGroup(ua string) *group {
	ual := strings.ToLower(ua)
	var specific *group
	var star *group
	for i := range f.groups {
		g := &f.groups[i]
		for _, a := range g.agents {
			if a == "*" {
				if star == nil {
					star = g
				}
				continue
			}
			if strings.Contains(ual, a) {
				specific = g
			}
		}
	}
	if specific != nil {
		return specific
	}
	return star
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		if strings.HasPrefix(raw, "/") {
			return raw
		}
		return "/" + raw
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// matchLen — длина совпавшего шаблона (в рунах паттерна) или -1.
// * — любая последовательность; $ — конец строки.
func matchLen(pat, path string) int {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return -1
	}
	anchoredEnd := strings.HasSuffix(pat, "$")
	if anchoredEnd {
		pat = strings.TrimSuffix(pat, "$")
	}
	ok := globMatch(pat, path, anchoredEnd)
	if !ok {
		return -1
	}
	return len([]rune(pat))
}

func globMatch(pat, s string, end bool) bool {
	// DP по байтам достаточно: * и литералы ASCII/UTF-8 как последовательность байт.
	p, n := []byte(pat), []byte(s)
	type key struct{ i, j int }
	memo := map[key]bool{}
	var rec func(i, j int) bool
	rec = func(i, j int) bool {
		k := key{i, j}
		if v, ok := memo[k]; ok {
			return v
		}
		if i == len(p) {
			r := !end || j == len(n)
			if !end {
				r = true // префикс: остаток пути допустим
			}
			memo[k] = r
			return r
		}
		if p[i] == '*' {
			// * пусто или съесть байт
			if rec(i+1, j) || (j < len(n) && rec(i, j+1)) {
				memo[k] = true
				return true
			}
			memo[k] = false
			return false
		}
		if j < len(n) && p[i] == n[j] && rec(i+1, j+1) {
			memo[k] = true
			return true
		}
		memo[k] = false
		return false
	}
	return rec(0, 0)
}

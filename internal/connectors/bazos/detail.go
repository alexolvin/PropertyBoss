// detail.go — детальный проход: разбор страницы объявления (ТЗ v4 §4.1).
//
// Структура страницы проверена 2026-09-06 на четырёх реальных
// объявлениях reality.bazos.cz (222871645, 222453921, 223071667,
// 223028684):
//   - заголовок: <H1 class=nadpisdetail>…</H1> в блоке inzeratydetnadpis;
//   - описание: <div class=popisdetail>…</div> — свободный текст +
//     ОПЦИОНАЛЬНЫЙ структурированный блок пар «LABEL:<br>VALUE» (в выборке
//     2026-09-06 блок есть у 1 из 4 объявлений — большинство полей
//     извлекается правилами из текста, см. rules.go);
//   - цена: блок «Cena:» (единственный на странице), в значениях
//     встречаются zero-width-символы — снимаются перед парсингом;
//   - продавец: «Jméno:» + <span class="paction">ИМЯ</span>;
//   - локация: ссылка «Přibližná lokalita» —
//     maps/place/LAT,LON/@LAT,LON,ZOOMz + PSČ (текст ссылки) + город;
//   - фото: img/{N}/{id3}/{id}.jpg (полный) и img/{N}t/… (миниатюра).
//
// Если верстка изменится — ParseDetail честно вернёт ошибку (layout
// change, ТЗ §8.2.2), а не пустые данные.
package bazos

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"propertyboss/internal/ratelimit"
)

// Сигналы делистинга с детальной страницы (ТЗ §8.2, та же классификация,
// что urlCheck delist-пасса):
//   - ErrListingGone — объявления больше нет: прямой 404/410 либо
//     редирект увёл с детальной страницы на чужую (раздел). Причина:
//     'unknown'.
//   - ErrListingRelisted — URL объявления редиректит на другое
//     объявление того же домена: объект перезалистился. Причина:
//     'relisted'.
//
// 429 — отдельный сигнал (ErrRateLimited, ТЗ §10.4): жёсткий лимит
// источника, прогон останавливается немедленно, источник — в cooldown.
// Прочие неконклюзивные ответы (403/5xx, сеть) остаются обычными
// ошибками: объект повторится следующим прогоном.
var (
	ErrListingGone     = errors.New("bazos: объявление отсутствует (listing gone)")
	ErrListingRelisted = errors.New("bazos: объявление перезалищено (relisted)")
	ErrRateLimited     = errors.New("bazos: rate limited (HTTP 429)")
)

// RateLimitError — HTTP 429 с указанием источника. Retry-After (если
// площадка его дала) — её собственная рекомендация, по которой
// вызывающий удлиняет cooldown, если он больше стандартного бэкоффа.
type RateLimitError struct {
	URL        string
	RetryAfter time.Duration // 0 — заголовка нет / не распознан
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%v: %s (retry-after %s)", ErrRateLimited, e.URL, e.RetryAfter.Round(time.Second))
	}
	return fmt.Sprintf("%v: %s", ErrRateLimited, e.URL)
}

// Unwrap — errors.Is(err, ErrRateLimited).
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// parseRetryAfter — Retry-After по RFC 7231: целые секунды либо HTTP-дата.
// Не распознано → 0 (вызывающий применяет стандартный бэкофф).
func parseRetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if sec, err := strconv.Atoi(h); err == nil && sec >= 0 {
		return time.Duration(sec) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
		return 0
	}
	return 0
}

// Detail — данные со страницы объявления (детальный проход, ТЗ v4 §4.1).
// Поля-указатели: nil — значение на странице не найдено (догадка
// запрещена, ТЗ v4 §0.4).
type Detail struct {
	Title        string
	Description  string // полный текст (popisdetail), разметка снята
	PriceMinor   *int64 // минорные единицы (CZK — 1/100)
	Currency     string
	Postcode     string // текст ссылки «Přibližná lokalita» (PSČ)
	City         string // городская ссылка рядом со ссылкой на карту
	SellerName   string
	SellerUserID string
	Photos       []string // полные URL в порядке сайта, без дублей
	SourceLat    *float64 // «Přibližná lokalita» — приблизительная локация
	SourceLng    *float64
	SourceZoom   *int
	Views        *int
	// Структурированный блок описания (source_field, уверенность 1.0):
	// Plocha / Stavba / Energetická náročnost / Vlastnictví /
	// Infrastruktura / Lokalita / Příslušenství / Ostatní / Celková cena.
	Block map[string]string
}

// blockLabels — метки структурированного блока (проверено 2026-09-06).
// Метка — отдельная строка, завершающаяся двоеточием; значение — строки
// до следующей метки (может быть многострочным, например Příslušenství).
var blockLabels = []string{
	"Plocha", "Stavba", "Energetická náročnost", "Vlastnictví",
	"Infrastruktura", "Lokalita", "Příslušenství", "Ostatní", "Celková cena",
}

var (
	// zeroWidth — анти-скрапинг: в числовых значениях (цены) площадка
	// вставляет невидимые символы; перед парсингом снимаются.
	zeroWidth = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "")

	reH1       = regexp.MustCompile(`(?s)<H1 class="?nadpisdetail"?>(.*?)</H1>`)
	rePopisDet = regexp.MustCompile(`<div class="?popisdetail"?>`)
	reBr       = regexp.MustCompile(`(?i)<br\s*/?>`)
	rePriceDet = regexp.MustCompile(`Cena:</td><td colspan=2><br><b>(?:<span[^>]*>)?\s*([\d][\d\s]*)\s*(Kč|CZK|€|EUR)`)
	reSeller   = regexp.MustCompile(`class="?paction"?>([^<]*)</span>`)
	rePhoto    = regexp.MustCompile(`https://www\.bazos\.cz/img/(\d+)(t?)/\d+/\d+\.jpg[^\s"']*`)
	reMap      = regexp.MustCompile(`maps/place/(-?\d+\.\d+),(-?\d+\.\d+)/@-?\d+\.\d+,-?\d+\.\d+,(\d+)z`)
	reCity     = regexp.MustCompile(`<a href="https://reality\.bazos\.cz/inzeraty/[^"]+">([^<]+)</a>`)
	rePSČ      = regexp.MustCompile(`title="Přibližná lokalita" rel="nofollow">([^<]+)</a>`)
	reViews    = regexp.MustCompile(`Vidělo:</td><td colspan=2><br>\s*(\d+)\s*lidí`)
	reLineWS   = regexp.MustCompile(`[\s ]+`)
)

// ParseDetail разбирает HTML страницы объявления. Чистая функция
// (тестируется на зафиксированных образцах, без сети).
func ParseDetail(page string) (*Detail, error) {
	page = zeroWidth.Replace(page)

	d := &Detail{Block: map[string]string{}}

	m := reH1.FindStringSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("bazos: детальная страница: заголовок (nadpisdetail) не найден — вероятно, изменена верстка (layout change)")
	}
	d.Title = htmlToText(m[1])
	if d.Title == "" {
		return nil, fmt.Errorf("bazos: детальная страница: заголовок пуст — вероятно, изменена верстка (layout change)")
	}

	// Описание — до первого </div>: в выборке 2026-09-06 вложенных div
	// внутри popisdetail нет; при изменении верстки это заметится по
	// отсутствию известных блоков/текста (честная ошибка выше).
	if i := rePopisDet.FindStringIndex(page); i != nil {
		rest := page[i[1]:]
		if j := strings.Index(rest, "</div>"); j >= 0 {
			d.Description = htmlToTextBr(rest[:j])
		} else {
			d.Description = htmlToTextBr(rest)
		}
	}
	if d.Description == "" {
		return nil, fmt.Errorf("bazos: детальная страница: описание (popisdetail) не найдено — вероятно, изменена верстка (layout change)")
	}
	parseBlock(d)

	// Цена: единственный блок «Cena:» на странице (проверено 2026-09-06).
	if m := rePriceDet.FindStringSubmatch(page); m != nil {
		num := strings.ReplaceAll(m[1], " ", "")
		if v, err := strconv.ParseInt(num, 10, 64); err == nil {
			cur := "CZK"
			if m[2] == "€" || m[2] == "EUR" {
				cur = "EUR"
			}
			minor := v * 100 // CZK/EUR: exponent 2 (таблица currencies)
			d.PriceMinor = &minor
			d.Currency = cur
		}
	}

	// Продавец: имя — в span после «Jméno:».
	if i := strings.Index(page, "Jméno:"); i >= 0 {
		if m := reSeller.FindStringSubmatch(page[i:]); m != nil {
			d.SellerName = html.UnescapeString(strings.TrimSpace(m[1]))
			if ui := regexp.MustCompile(`odeslatakci\('rating','(\d+)'`).FindStringSubmatch(page[i:]); ui != nil {
				d.SellerUserID = ui[1]
			}
		}
	}

	// Локация: ссылка «Přibližná lokalita» (LAT,LON,ZOOM) + PSČ + город.
	if m := reMap.FindStringSubmatch(page); m != nil {
		lat, _ := strconv.ParseFloat(m[1], 64)
		lng, _ := strconv.ParseFloat(m[2], 64)
		zoom, _ := strconv.Atoi(m[3])
		d.SourceLat, d.SourceLng, d.SourceZoom = &lat, &lng, &zoom
	}
	if m := rePSČ.FindStringSubmatch(page); m != nil {
		d.Postcode = strings.TrimSpace(m[1])
	}
	if i := reMap.FindStringIndex(page); i != nil {
		if m := reCity.FindStringSubmatch(page[i[1] : i[1]+400]); m != nil {
			d.City = html.UnescapeString(strings.TrimSpace(m[1]))
		}
	}
	if m := reViews.FindStringSubmatch(page); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			d.Views = &v
		}
	}

	// Фотографии: полные (без «t») в порядке первой встречи.
	seen := map[int]bool{}
	for _, m := range rePhoto.FindAllStringSubmatch(page, -1) {
		if m[2] != "" {
			continue // миниатюра (img/{N}t/)
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || seen[n] {
			continue
		}
		seen[n] = true
		d.Photos = append(d.Photos, m[0])
	}
	return d, nil
}

// htmlToTextBr — HTML → текст с сохранением <br> как перевода строк.
func htmlToTextBr(s string) string {
	s = reBr.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		b.WriteString(reLineWS.ReplaceAllString(strings.TrimSpace(line), " "))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func htmlToText(s string) string {
	return reLineWS.ReplaceAllString(html.UnescapeString(reTag.ReplaceAllString(s, " ")), " ")
}

// parseBlock — структурированный блок описания: строка-метка с
// двоеточием, значение — строки до следующей метки.
func parseBlock(d *Detail) {
	lines := strings.Split(d.Description, "\n")
	labelIdx := map[string]int{} // метка (без «:», нижний регистр) → позиция
	for i, l := range lines {
		t := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(l), ":"))
		if _, ok := labelIdx[t]; !ok {
			for _, lab := range blockLabels {
				if t == strings.ToLower(lab) {
					labelIdx[t] = i
					break
				}
			}
		}
	}
	type span struct {
		label    string
		from, to int
	}
	var spans []span
	for _, lab := range blockLabels {
		for k, i := range labelIdx {
			if k == strings.ToLower(lab) {
				spans = append(spans, span{lab, i, len(lines)})
			}
		}
	}
	// Отсортировано по положению (порядок blockLabels — канонический,
	// но метки на странице могут идти в любом порядке).
	for i := 0; i < len(spans); i++ {
		for j := i + 1; j < len(spans); j++ {
			if spans[j].from < spans[i].from {
				spans[i], spans[j] = spans[j], spans[i]
			}
		}
	}
	for i, sp := range spans {
		end := sp.to
		if i+1 < len(spans) {
			end = spans[i+1].from
		}
		var val []string
		for _, l := range lines[sp.from+1 : end] {
			if t := strings.TrimSpace(l); t != "" {
				val = append(val, t)
			}
		}
		if len(val) > 0 {
			d.Block[sp.label] = strings.Join(val, "; ")
		}
	}
}

// DetailClient — HTTP-клиент для страниц объявлений (отдельный таймаут
// из конфига detail.timeout_sec, не общий таймаут категории).
type DetailClient struct {
	http      *http.Client
	userAgent string
}

// NewDetailClient создаёт клиент; timeout — detail.timeout_sec.
func NewDetailClient(timeout time.Duration) *DetailClient {
	return &DetailClient{
		http:      &http.Client{Timeout: timeout},
		userAgent: userAgent,
	}
}

// Fetch — страница объявления по полному URL (real-URL из raw_listings).
//
// Редиректы следует (клиент по умолчанию). Удалённые объявления bazos
// отвечает 301 на страницу раздела (подтверждено 07.09: выборка 30/30
// pending-объектов → /inzeraty/<slug>/), поэтому финальный URL
// классифицируется по ТЗ §8.2:
//   - 404/410 → ErrListingGone;
//   - 2xx, финальный путь ≠ запрошенному: начинается с /inzerat/ —
//     другое объявление того же домена → ErrListingRelisted; иначе —
//     увело с объявления (раздел) → ErrListingGone;
//   - 2xx, путь без изменений → страница объявления;
//   - 429 → ErrRateLimited (RateLimitError): жёсткий лимит источника
//     (ТЗ §10.4) — не «повторить позже», а стоп-сигнал прогону;
//   - прочие 3xx/403/5xx → обычная ошибка (повторить позже).
func (c *DetailClient) Fetch(ctx context.Context, url string) (string, error) {
	// D20: общий ограничитель частоты запросов к источнику — путь
	// обогащения, контроля и верификации.
	if err := ratelimit.WaitSource(ctx); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("bazos: detail: запрос %s: %w", url, err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept-Language", "cs,en;q=0.7")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("bazos: detail: %s: %w", url, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// 404/410 — исчезновение подтверждено (ТЗ §8.2).
		io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("%w: %s: HTTP %d", ErrListingGone, url, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		// 429 — жёсткий лимит источника (ТЗ §10.4): стоп-сигнал,
		// а не «повторить позже» (D13). Дальнейшие запросы до конца
		// кулдауна запрещены — иначе IP уходит в блокировку.
		io.Copy(io.Discard, resp.Body)
		return "", &RateLimitError{URL: url, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	case resp.StatusCode != http.StatusOK:
		io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("bazos: detail: %s: HTTP %d", url, resp.StatusCode)
	}
	// Классификация редиректа: страница объявления — путь /inzerat/…,
	// страница раздела — /inzeraty/… (проверено 07.09).
	if final := resp.Request.URL; final.Path != req.URL.Path {
		io.Copy(io.Discard, resp.Body)
		if strings.HasPrefix(final.Path, "/inzerat/") {
			return "", fmt.Errorf("%w: %s → %s", ErrListingRelisted, url, final)
		}
		return "", fmt.Errorf("%w: %s → %s", ErrListingGone, url, final)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 МБ потолок страницы
	if err != nil {
		return "", fmt.Errorf("bazos: detail: %s: чтение: %w", url, err)
	}
	return string(body), nil
}

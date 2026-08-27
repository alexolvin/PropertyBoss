// Package bazos — первый simple-коннектор: reality.bazos.cz (CZ).
//
// Данные берутся со страниц категорий раздела «Недвижимость»
// (продажа — /prodam/…, аренда — /pronajmu/…, подкатегория по
// property_type, полная пагинация по 20 карточек на страницу).
//
// ТЗ §13: до написания коннектора зафиксирован sources.access_policy
// (sources id 'bazos-reality', проверено 2026-08-27): официального API
// нет; robots.txt разрешает страницы категорий и объявлений для общего
// user-agent (запрещены только URL-параметры поиска/фильтров и
// сервисные .php); в ToS нет пункта о запрете автоматизированного
// доступа. Обход технических мер защиты не применяется (ТЗ §13).
//
// Ограничения этапа 3 (честно, ТЗ §0.4 — NULL + причина):
//   - только данные карточки со страницы категории: ссылка, дата,
//     выдержка описания, цена, город + PSČ;
//   - координат нет: сайт публикует лишь примерную локацию на странице
//     детального объявления (ссылка на Google-карту «Přibližná
//     lokalita») — выгрузка детальных страниц — расширение этапа 9;
//   - площадь и комнаты в карточке не публикуются — поля nil;
//   - фильтры цены/площади из search_configs не поддерживаются:
//     robots.txt запрещает URL-параметры (?cenaod=, ?cenado=, ?hledat=…),
//     а клиентская фильтрация искажала бы детектирование исчезновений
//     (ТЗ §8.2) — коннектор возвращает явную ошибку.
package bazos

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"propertyboss/internal/scan"
)

// SourceID — id источника в таблице sources.
const SourceID = "bazos-reality"

const (
	// Домен категории «Недвижимость» (проверено 2026-08-27: раздел
	// существует на www.bazos.cz как отдельный поддомен).
	baseURL = "https://reality.bazos.cz"
	// Честный описательный User-Agent (проверено 2026-08-27: страницы
	// категорий отвечают 200).
	userAgent = "PropertyBoss/0.3 (real-estate price monitoring)"
	// Карточек на странице категории (проверено 2026-08-27:
	// «Zobrazeno 1-20 inzerátů z …»).
	pageSize = 20
	langCS   = "cs"
)

// dealByType — deal_type (search_configs) → префикс URL категории.
// Подтверждено на сайте 2026-08-27 (раздел «Недвижимость»).
var dealByType = map[string]string{
	"sale": "prodam",
	"rent": "pronajmu",
}

// categoryByType — канонический property_type системы → подкатегория
// bazos. Список подкатегорий подтверждён на сайте 2026-08-27 (меню
// раздела; у аренды есть и «podnajem» — в каноническую таксономию не
// входит, коннектор его не сканирует).
var categoryByType = map[string]string{
	"flat":       "byt",
	"house":      "dum",
	"land":       "pozemek",
	"garage":     "garaz",
	"office":     "kancelar",
	"cottage":    "chata",
	"space":      "prostory",
	"project":    "projekty",
	"restaurant": "restaurace",
	"warehouse":  "sklad",
	"garden":     "zahrada",
	"other":      "ostatni",
}

// Регулярки структуры карточки (страница категории, проверено
// 2026-08-27 на реальном HTML; образец — internal/connectors/bazos
// тесты). Если верстка изменится — распарсивание честно сломается
// (layout_change, ТЗ §8.2.2), а не выдаст пустые данные.
var (
	cardStart  = `<div class="inzeraty inzeratyflex">`
	// m[1] — путь объявления, m[2] — числовой id.
	reHref     = regexp.MustCompile(`<div class="inzeratynadpis"><a href="(/inzerat/(\d+)/[^"]+\.php)"`)
	reDate     = regexp.MustCompile(`\[(\d{1,2})\.(\d{1,2})\. (\d{4})\]`)
	rePopis    = regexp.MustCompile(`(?s)<div class=popis>(.*?)</div>`)
	rePriceDiv = regexp.MustCompile(`(?s)<div class="inzeratycena">.*?</div>`)
	rePriceNum = regexp.MustCompile(`([\d][\d \x{00A0}]*)\s*Kč`)
	reLok      = regexp.MustCompile(`(?s)<div class="inzeratylok">(.*?)</div>`)
	reTotal    = regexp.MustCompile(`Zobrazeno \d+-\d+ inzerát?ů z ([\d \x{00A0}]+)`)
	reTag      = regexp.MustCompile(`<[^>]+>`)
	reSpace    = regexp.MustCompile(`[\s\x{00A0}]+`)
)

// noPriceTokens — состояния «числовой цены нет» на источнике:
// PriceMinor = nil (честный NULL, ТЗ §0.4), а не ошибка.
// Источник значений: полный сбор со всех страниц /prodam/byt/
// (384 страницы, 7 677 карточек, 2026-08-27): Dohodou — 309,
// V textu — 302, Nabídněte — 54, Zdarma — 8, Nerozhoduje — 2
// (значение не идентифицировано, контекст карточек не изучался).
// Любое другое нераспознанное состояние цены — layout_change
// (ТЗ §8.2.2): новый токен должен заметить человек, а не скан
// молча записать NULL. Словарь собран для категории «byt»; для
// других категорий токен вне списка тоже честно пометится.
var noPriceTokens = map[string]bool{
	"V textu":     true, // цена в тексте объявления
	"Dohodou":     true, // цена по договорённости
	"Nabídněte":   true, // «сделайте предложение»
	"Zdarma":      true, // «бесплатно» — число не публикуется
	"Nerozhoduje": true, // см. источник значений в комментарии
}

// priceText — нормализованный текст div цены (без разметки) для
// сопоставления с noPriceTokens.
func priceText(pd string) string {
	t := html.UnescapeString(reTag.ReplaceAllString(pd, " "))
	return strings.TrimSpace(reSpace.ReplaceAllString(t, " "))
}

// Connector — simple-коннектор reality.bazos.cz.
type Connector struct {
	http  *http.Client
	delay time.Duration // пауза между страницами (config: scan.page_delay_ms)
}

func init() {
	scan.Register(&Connector{
		http:  &http.Client{Timeout: 60 * time.Second},
		delay: time.Second, // значение по умолчанию; бинарь поправит из конфига
	})
}

// SetPageDelay — пауза между запросами страниц; вызывает бинарь после
// загрузки конфига (не часть контракта scan.Connector — у разных
// коннекторов разные параметры вежливости).
func (c *Connector) SetPageDelay(d time.Duration) { c.delay = d }

func (c *Connector) SourceID() string { return SourceID }

// Scan — полный проход по всем страницам одной категории.
//
// Контракт scan.Connector:
//   - (listings, nil, nil) — все страницы загружены;
//   - (listings, issue, nil) — часть страниц не загружена / распознана
//     честно помечена неполной (ТЗ §8.2.1);
//   - (nil, nil, err) — скан не состоялся.
func (c *Connector) Scan(ctx context.Context, cfg scan.SearchConfig) ([]scan.Listing, *scan.Issue, error) {
	// Явные ошибки вместо молчаливого игнора (ТЗ §0.2).
	if cfg.MinPriceMinor != nil || cfg.MaxPriceMinor != nil ||
		cfg.MinAreaSqM != nil || cfg.MaxAreaSqM != nil {
		return nil, nil, fmt.Errorf(
			"bazos: фильтры цены/площади не поддерживаются (конфигурация %d): robots.txt запрещает URL-параметры фильтров, клиентская фильтрация искажает детектирование исчезновений (ТЗ §8.2); уберите min/max_price_minor и min/max_area_sqm", cfg.ID)
	}
	if cfg.PropertyType == nil {
		return nil, nil, fmt.Errorf("bazos: в конфигурации %d не задан property_type: коннектор сканирует только конкретную категорию раздела", cfg.ID)
	}
	deal, ok := dealByType[cfg.DealType]
	if !ok {
		return nil, nil, fmt.Errorf("bazos: неизвестный deal_type %q (ожидается sale|rent)", cfg.DealType)
	}
	cat, ok := categoryByType[strings.ToLower(*cfg.PropertyType)]
	if !ok {
		return nil, nil, fmt.Errorf("bazos: property_type %q не соответствует ни одной категории раздела (поддерживаются: flat, house, land, garage, office, cottage, space, project, restaurant, warehouse, garden, other)", *cfg.PropertyType)
	}

	// Страница 1: базовый URL категории.
	body, err := c.fetchPage(ctx, baseURL+"/"+deal+"/"+cat+"/")
	if err != nil {
		return nil, nil, err
	}
	total, totalOK := parseTotal(body)
	listings, problems := parseCards(body)
	annotate(listings, cfg)

	switch {
	case len(listings) == 0 && totalOK && total == 0:
		// Категория пуста по счётчику — честная пустая выдача
		// (Runner пометит scan partial, ТЗ §8.2.1).
		return []scan.Listing{}, nil, nil
	case len(listings) == 0:
		msg := "на первой странице категории нет распознанных карточек"
		if totalOK {
			msg += fmt.Sprintf(" (счётчик страниц говорит о %d объявлениях)", total)
		}
		return nil, &scan.Issue{Kind: scan.FailLayoutChange, Message: msg}, nil
	case !totalOK:
		// Карточки есть, но счётчик «z N» не найден — число страниц
		// определить нельзя, полный скан невозможен.
		return listings, &scan.Issue{Kind: scan.FailLayoutChange,
			Message: "счётчик общего числа объявлений не распознан — число страниц неизвестно"}, nil
	}

	pages := (total + pageSize - 1) / pageSize
	for p := 2; p <= pages; p++ {
		if c.delay > 0 {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(c.delay):
			}
		}
		u := fmt.Sprintf("%s/%s/%s/%d/", baseURL, deal, cat, (p-1)*pageSize)
		body, err := c.fetchPage(ctx, u)
		if err != nil {
			// Часть уже получена — неполный скан (ТЗ §8.2.1).
			return listings, &scan.Issue{Kind: scan.Classify(err),
				Message: fmt.Sprintf("страница %d из %d не загружена: %v", p, pages, err)}, nil
		}
		pageListings, pageProblems := parseCards(body)
		annotate(pageListings, cfg)
		problems = append(problems, pageProblems...)
		if len(pageListings) == 0 {
			// Выдача закончилась раньше, чем обещал счётчик (объявления
			// удалили во время скана) — дальше идти некуда.
			break
		}
		listings = append(listings, pageListings...)
	}

	if len(problems) > 0 {
		shown := problems
		if len(shown) > 3 {
			shown = shown[:3]
		}
		return listings, &scan.Issue{Kind: scan.FailLayoutChange,
			Message: fmt.Sprintf("%d карточек не распознаны полностью: %s", len(problems), strings.Join(shown, "; "))}, nil
	}
	return listings, nil, nil
}

// fetchPage — GET страницы с проверкой кода ответа и кодировки.
// Кодировка не гадается (ТЗ §0.4): ожидается именно UTF-8
// (проверено 2026-08-27: text/html; charset=UTF-8).
func (c *Connector) fetchPage(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", scan.NewFail(scan.FailNetwork, err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", scan.NewFail(scan.FailNetwork, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", scan.NewFailf(scan.FailHTTP429, "GET %s: HTTP 429 (частота запросов ограничена)", u)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		// 403/401 — возможная блокировка; без HTML не отличить капчу,
		// помечаем как captcha-подозрение, если тело похоже на проверку,
		// иначе network (честно: тип блокировки не установлен).
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if strings.Contains(strings.ToLower(string(body)), "captcha") {
			return "", scan.NewFailf(scan.FailCaptcha, "GET %s: HTTP %d с текстом captcha", u, resp.StatusCode)
		}
		return "", scan.NewFailf(scan.FailNetwork, "GET %s: HTTP %d (доступ запрещён?)", u, resp.StatusCode)
	case resp.StatusCode/100 != 2:
		return "", scan.NewFailf(scan.FailNetwork, "GET %s: HTTP %d", u, resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(ct), "charset=utf-8") {
		return "", scan.NewFailf(scan.FailLayoutChange, "GET %s: неожиданная кодировка в Content-Type %q (ожидается utf-8)", u, ct)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return "", scan.NewFailf(scan.FailNetwork, "GET %s: чтение тела: %v", u, err)
	}
	return string(body), nil
}

// parseTotal — общее число объявлений по счётчику «Zobrazeno 1-20
// inzerátů z 7 678». false — счётчик не найден.
func parseTotal(pageHTML string) (int, bool) {
	m := reTotal.FindStringSubmatch(pageHTML)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(reSpace.ReplaceAllString(m[1], ""))
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseCards — все карточки из HTML страницы. Возвращает записи и
// список проблем (карточки, где не распознано поле) — для честного
// layout_change-пометка на уровне скана.
func parseCards(pageHTML string) ([]scan.Listing, []string) {
	var out []scan.Listing
	var problems []string
	rest := pageHTML
	for {
		i := strings.Index(rest, cardStart)
		if i < 0 {
			break
		}
		rest = rest[i+len(cardStart):]
		j := strings.Index(rest, cardStart)
		seg := rest
		if j >= 0 {
			seg = rest[:j]
		}
		l, prob := parseCard(seg)
		if l.ExternalID != "" {
			out = append(out, l)
		}
		if prob != "" {
			problems = append(problems, prob)
		}
	}
	return out, problems
}

// annotate — поля, общие всем карточкам одной категории: канонический
// property_type (карточка сайта его не несёт — категория задана в
// конфигурации) и язык оригинала.
func annotate(listings []scan.Listing, cfg scan.SearchConfig) {
	lang := langCS
	for i := range listings {
		listings[i].PropertyType = cfg.PropertyType
		listings[i].LanguageOriginal = &lang
	}
}

// parseCard — одна карточка. prob — описание проблемы (не распознана
// карточка целиком или цена в неизвестном формате); nil/"" — чисто.
func parseCard(seg string) (scan.Listing, string) {
	var l scan.Listing
	m := reHref.FindStringSubmatch(seg)
	if m == nil {
		return l, "нет ссылки на объявление /inzerat/…"
	}
	l.ExternalID = m[2]
	l.URL = baseURL + m[1]

	// Дата публикации: «[27.8. 2026]» (день может быть однозначным).
	if md := reDate.FindStringSubmatch(seg); md != nil {
		if t, err := time.Parse("2.1. 2006", md[1]+"."+md[2]+". "+md[3]); err == nil {
			t = t.In(time.Local)
			l.PostedAt = &t
		} else {
			return l, fmt.Sprintf("объявление %s: не распознана дата публикации", l.ExternalID)
		}
	}

	// Цена: «14 500 000 Kč» (CZK, exponent 2 — халержи, ISO 4217)
	// либо состояние «числовой цены нет» (noPriceTokens) — в обоих
	// случаях без числа PriceMinor = nil (честный NULL, ТЗ §0.4).
	// Отсутствующий div цены или нераспознанное состояние — problem
	// (layout_change, ТЗ §8.2.2).
	pd := rePriceDiv.FindString(seg)
	if pd == "" {
		return l, fmt.Sprintf("объявление %s: div цены не найден (смена вёрстки?)", l.ExternalID)
	}
	if mp := rePriceNum.FindStringSubmatch(pd); mp != nil {
		n, err := strconv.ParseInt(reSpace.ReplaceAllString(mp[1], ""), 10, 64)
		if err != nil {
			return l, fmt.Sprintf("объявление %s: не распознана цена %q", l.ExternalID, mp[1])
		}
		minor := n * 100 // CZK: exponent 2 (money: ISO 4217)
		ccy := "CZK"
		l.PriceMinor = &minor
		l.Currency = &ccy
	} else if !noPriceTokens[priceText(pd)] {
		return l, fmt.Sprintf("объявление %s: нераспознанное состояние цены %q", l.ExternalID, priceText(pd))
	}

	// Локация: «Praha 5<br>152 00» → «Praha 5, 152 00». Улицы в карточке
	// нет — полный адрес даёт только детальная страница (этап 9).
	if ml := reLok.FindStringSubmatch(seg); ml != nil {
		loc := html.UnescapeString(strings.TrimSpace(strings.ReplaceAll(ml[1], "<br>", ", ")))
		if loc != "" {
			l.Address = &loc
		}
	}

	// Выдержка описания (на странице категории обрезана «…»).
	if mp := rePopis.FindStringSubmatch(seg); mp != nil {
		desc := reTag.ReplaceAllString(mp[1], " ")
		desc = html.UnescapeString(desc)
		desc = strings.TrimSpace(reSpace.ReplaceAllString(desc, " "))
		if desc != "" {
			l.Description = &desc
		}
	}
	return l, ""
}

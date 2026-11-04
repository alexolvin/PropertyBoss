// enrich — детальный проход по объектам (ТЗ v4 §4.1, §4.2, §4.5, §4.7).
//
// `pb enrich run` выбирает активные объекты с объявлением на bazos.cz,
// чья детальная страница ещё не обработана (detail_fetched_at IS NULL),
// и по каждому:
//
//  1. скачивает страницу объявления (пауза detail.page_delay_ms —
//     вежливость, как в сканере);
//  2. разбирает её (bazos.ParseDetail: цена, описание, продавец, фото,
//     координаты карты, структурированный блок);
//  3. выводит атрибуты (source_field/правила, bazos.Derive) →
//     objects.attributes + provenance в attribute_values;
//  4. определяет тип продавца по маркерам (ТЗ §4.7, шаг 2 цепочки;
//     шаг 3 — LLM, отдельный прогон `pb extract`);
//  5. геокодирует (ТЗ §4.5): координаты площадки (zoom → уровень) →
//     Nominatim;
//  6. пишет фотографии: оригинальные URL → object_photos
//     (локальные копии — отдельный прогон `pb photos run`);
//  7. контрольная цена: цена детали ≠ текущая цена объекта →
//     price_history (current_price_minor трогать не будем — он за
//     сканером).
//
// Честная обработка (ТЗ v4 §0.4): ошибка fetch/parse — объект НЕ
// отмечается обработанным (повторится следующим прогоном); координаты
// не получены — объект без точек, не ошибка; маркеров продавца нет —
// seller_type NULL (догадка запрещена).
package enrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/attrules"
	"propertyboss/internal/config"
	"propertyboss/internal/connectors/bazos"
	"propertyboss/internal/extract"
	"propertyboss/internal/geocode"
	"propertyboss/internal/schedule"
)

// Report — результат прогона.
type Report struct {
	Objects int `json:"objects"` // кандидатов
	Done    int `json:"done"`    // деталей записано
	// Delisted — объявление исчезло по ПРЯМОМУ свидетельству площадки
	// (ТЗ §8.2): 301 на раздел/404/410 или редирект на другое объявление
	// при загрузке страницы. Результат, а не ошибка (C7): не проходит
	// через delist.max_delisted_share_pct — его защита контрольные
	// объекты; без контрольных — fallback на гвард (DelistAnomaly).
	Delisted int `json:"delisted"`
	// Verified — объекты, статус объявления которых верифицирован в
	// раунде (fetch вернул однозначный ответ: живо / исчезло /
	// перезалист). D18: единое основание для Delisted и доли в
	// anomaly-уведомлении — доля считается от Verified, а не от Objects
	// (в него входят кандидаты, не верифицированные в раунде).
	Verified   int `json:"verified"`
	Failed     int `json:"failed"`      // fetch/parse/DB-ошибок (повторятся)
	GeocodeErr int `json:"geocode_err"` // ошибок геокодера (best effort)
	Geocoded   int `json:"geocoded"`    // объектов, получивших координаты
	SellerType int `json:"seller_type"` // типов продавца установлено
	Attrs      int `json:"attrs"`       // строк attribute_values
	Photos     int `json:"photos"`      // строк object_photos
	PriceDiff  int `json:"price_diff"`  // цена детали ≠ текущая цена
	// LLM (ТЗ v4 §4.3, шаг 3): только ulovdomov-импорт вызывает
	// extract.ProcessObject сразу после детали. Bazos по-прежнему
	// отдельным `pb extract run`.
	LLMCalls int `json:"llm_calls"`
	LLMValid int `json:"llm_valid"`
	LLMErr   int `json:"llm_err"`
	// Контрольные объекты (C7): запрошено / подтвердили живость.
	// ControlsChecked == 0 — контрольных нет (fallback на гвард).
	ControlsChecked int `json:"controls_checked"`
	ControlsAlive   int `json:"controls_alive"`
	// DelistAnomaly — сработал аномалий-гвард (ТЗ §8.2, защита №3) в
	// fallback-режиме БЕЗ контрольных: доля delisted превысила
	// delist.max_delisted_share_pct, дальнейший delisting не применялся,
	// запись уведомления в notifications.
	DelistAnomaly bool `json:"delist_anomaly"`
	// CollectionFailed — отказ сбора (C7): контрольные объявления тоже
	// вернули 301/404/редирект — сигнал «объявление исчезло» ненадёжен
	// (сбой сайта/смена формата URL), прогон остановлен ДО обработки
	// кандидатов, delisting не применён, уведомление в notifications.
	CollectionFailed bool `json:"collection_failed"`
	// RateLimited — прогон остановлен по HTTP 429 (ТЗ §10.4, D13):
	// источник в cooldown, необработанные кандидаты повторятся
	// следующим прогоном (не ранее конца кулдауна).
	RateLimited bool `json:"rate_limited"`
}

// countryNames — код objects.country → название страны для запроса
// геокодера (Nominatim понимает полное название).
var countryNames = map[string]string{
	"CZ": "Czechia",
	"IT": "Italy",
	"NL": "Netherlands",
}

func countryName(code string) string {
	if n, ok := countryNames[code]; ok {
		return n
	}
	return code
}

// candidate — объект без детали и его объявление на площадке.
type candidate struct {
	id         int64
	country    string
	priceMinor *int64
	currency   *string
	sourceID   string
	url        string
}

// Run — бэкфилл детального прохода.
//
//	limit   — сколько объектов за прогон (0 — без лимита);
//	country — один рынок ("" — все; для bazos на практике всегда CZ).
//
// Частичный прогон — нормальный исход: необработанные остаются
// кандидатами для следующего прогона. Ошибка отдельного объекта
// (fetch/parse/DB) не останавливает прогон — считается и логируется.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, limit int, country string, objectID int64, sourceID string) (*Report, error) {
	rep := &Report{}

	det := bazos.NewDetailClient(time.Duration(cfg.Detail.TimeoutSec) * time.Second)
	// Геокодер — только если секция geocode сконфигурирована; без него
	// Resolve работает на координатах площадки (шаг 1).
	var gc geocode.Geocoder
	if cfg.Geocode.BaseURL != "" {
		gc = &geocode.Cached{
			Inner: geocode.NewClient(cfg.Geocode.BaseURL, cfg.Geocode.UserAgent, cfg.Geocode.RatePerMin, cfg.Geocode.TimeoutSec),
			Pool:  pool,
		}
	}

	// Ключи реестра по странам (кэш): разделение attributes /
	// attributes_unmapped по тому же правилу, что в сканере.
	regKeys := map[string]map[string]bool{}
	keysFor := func(c string) (map[string]bool, error) {
		if k, ok := regKeys[c]; ok {
			return k, nil
		}
		rows, err := pool.Query(ctx, `SELECT key FROM attribute_registry WHERE country = $1`, c)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		m := map[string]bool{}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return nil, err
			}
			m[k] = true
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		regKeys[c] = m
		return m, nil
	}

	// Движки шаблонного слоя (ТЗ v4 §4.2, гибридное извлечение): активные
	// шаблоны страны, кэш на прогон. Пустой (0 шаблонов) — Apply no-op.
	engines := map[string]*attrules.Engine{}
	engFor := func(c string) (*attrules.Engine, error) {
		if e, ok := engines[c]; ok {
			return e, nil
		}
		e, err := attrules.Load(ctx, pool, c)
		if err != nil {
			return nil, err
		}
		engines[c] = e
		return e, nil
	}

	// Кандидаты: последний (по fetched_at) bazos-линкинг на объект.
	q := `
SELECT DISTINCT ON (o.id)
       o.id, o.country, o.current_price_minor, o.currency,
       ol.source_id, rl.source_url
FROM objects o
JOIN object_listings ol ON ol.object_id = o.id
JOIN raw_listings rl ON rl.id = ol.raw_listing_id
WHERE o.detail_fetched_at IS NULL
  AND o.status = 'active'
  AND (rl.source_url LIKE '%bazos.cz%' OR rl.source_url LIKE '%ulovdomov.cz%')`
	var args []any
	if country != "" {
		q += "\n  AND o.country = $" + fmt.Sprintf("%d", len(args)+1)
		args = append(args, country)
	}
	if objectID > 0 {
		q += "\n  AND o.id = $" + fmt.Sprintf("%d", len(args)+1)
		args = append(args, objectID)
	}
	if sourceID != "" {
		q += "\n  AND ol.source_id = $" + fmt.Sprintf("%d", len(args)+1)
		args = append(args, sourceID)
	}
	q += "\nORDER BY o.id, rl.fetched_at DESC"
	if limit > 0 {
		q += "\nLIMIT $" + fmt.Sprintf("%d", len(args)+1)
		args = append(args, limit)
	}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("enrich: кандидаты: %w", err)
	}
	defer rows.Close()

	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.country, &c.priceMinor, &c.currency, &c.sourceID, &c.url); err != nil {
			return nil, fmt.Errorf("enrich: candidates scan: %w", err)
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("enrich: candidates: %w", err)
	}
	rep.Objects = len(cands)
	log.Printf("enrich: кандидатов %d", len(cands))
	if len(cands) == 0 {
		return rep, nil
	}

	// ТЗ §10.4 (D13): источник в активном cooldown — прогон не
	// запускается. Кулдаун задан 429-сигналом предыдущего прогона:
	// повторный обстрел до его конца запрещён (иначе лимит обходит
	// ручной перезапуск CLI).
	seenSrc := map[string]bool{}
	for _, c := range cands {
		if seenSrc[c.sourceID] {
			continue
		}
		seenSrc[c.sourceID] = true
		until, cerr := sourceInCooldown(ctx, pool, c.sourceID)
		if cerr != nil {
			return rep, fmt.Errorf("enrich: состояние источника %s: %w", c.sourceID, cerr)
		}
		if until.After(time.Now()) {
			log.Printf("enrich: источник %s в cooldown до %s (ТЗ §10.4) — прогон не запущен",
				c.sourceID, until.Format("2006-01-02 15:04"))
			return rep, nil
		}
	}

	// C7: контрольные объекты — защита делистинга по прямому
	// свидетельству (301/404/410 при загрузке страницы объявления).
	// Прямое свидетельство — НЕ вывод по отсутствию (delist-пасс),
	// поэтому delist.max_delisted_share_pct на него не распространяется;
	// вместо доли — проверка сбора ДО обработки пула:
	//   контрольные живы  → 301 подлинные, delisting без ограничения доли;
	//   контрольные упали → отказ сбора, прогон остановлен, delisting
	//                        не применён, уведомление оператору;
	//   контрольных нет   → консервативный fallback на max_delisted_share_pct.
	srcIDs := make([]string, 0, len(cands))
	inSrc := map[string]bool{}
	for _, c := range cands {
		if !inSrc[c.sourceID] {
			inSrc[c.sourceID] = true
			srcIDs = append(srcIDs, c.sourceID)
		}
	}
	var ctrlIDs []string
	for _, id := range srcIDs {
		if id == "bazos-reality" {
			ctrlIDs = append(ctrlIDs, id)
		}
	}
	unlimited, rateSrc, cerr := checkControls(ctx, pool, cfg, det, country, ctrlIDs, rep)
	if cerr != nil {
		return rep, cerr
	}
	if rateSrc != "" {
		// 429 на контрольном (ТЗ §10.4, D13): cooldown уже записан в
		// checkControls — прогон не запускается.
		log.Printf("enrich: HTTP 429 на контрольном объявлении — источник %q в cooldown, прогон не запущен (ТЗ §10.4)", rateSrc)
		return rep, nil
	}
	if rep.CollectionFailed {
		log.Printf("enrich: ОТКАЗ СБОРА: контрольные объявления тоже исчезли (%d/%d живы) — прогон остановлен до обработки кандидатов, delisting не применён (уведомление в notifications)",
			rep.ControlsAlive, rep.ControlsChecked)
		return rep, nil
	}
	if rep.ControlsChecked == 0 {
		log.Printf("enrich: контрольных объявлений нет — delisting под гвардом доля > %.0f%% (fallback)",
			cfg.Delist.MaxDelistedSharePct)
	}

	// Настройки бэкоффа (ТЗ §10.4) — те же, что у скан-пути: 429 на пути
	// обогащения переводит источник в cooldown той же механикой (D13).
	sched := schedule.SettingsFrom(cfg)

	delay := time.Duration(cfg.Detail.PageDelayMS) * time.Millisecond
	for i, c := range cands {
		if i > 0 && delay > 0 {
			select {
			case <-ctx.Done():
				return rep, ctx.Err()
			case <-time.After(delay):
			}
		}
		keys, err := keysFor(c.country)
		if err != nil {
			return rep, fmt.Errorf("enrich: реестр %s: %w", c.country, err)
		}
		eng, err := engFor(c.country)
		if err != nil {
			return rep, fmt.Errorf("enrich: шаблоны %s: %w", c.country, err)
		}
		if isUlovURL(c.url) {
			if err := processUlovObject(ctx, pool, cfg, c, keys, rep); err != nil {
				var perm *extract.ErrPermanent
				if errors.As(err, &perm) {
					return rep, err
				}
				rep.Failed++
				log.Printf("enrich: объект %d: %v", c.id, err)
			}
			continue
		}
		if err := processObject(ctx, pool, cfg, det, gc, keys, eng, c, unlimited, rep); err != nil {
			if errors.Is(err, bazos.ErrRateLimited) {
				// Жёсткий лимит источника (ТЗ §10.4): продолжать бить
				// запросами — значит увести IP в блокировку и обнулить
				// весь слой данных. Прогон останавливается немедленно,
				// источник — в cooldown (strikes +1, экспоненциальный
				// откат, rate_factor вдвое — как на скан-пути).
				rep.RateLimited = true
				strikes, until, berr := schedule.ApplyBackoff(ctx, pool, sched, c.sourceID, time.Now())
				if berr != nil {
					log.Printf("enrich: 429: запись cooldown: %v", berr)
				} else {
					log.Printf("enrich: HTTP 429 — источник %q в cooldown до %s (срыв #%d), прогон остановлен (ТЗ §10.4)",
						c.sourceID, until.Format("2006-01-02 15:04"), strikes)
					if rl := new(bazos.RateLimitError); errors.As(err, &rl) && rl.RetryAfter > 0 {
						// Рекомендация площадки: если Retry-After длиннее
						// стандартного бэкоффа — удлиняем cooldown до него.
						if ra := time.Now().Add(rl.RetryAfter); ra.After(until) {
							if _, uerr := pool.Exec(ctx,
								`UPDATE sources SET cooldown_until = $2 WHERE id = $1`,
								c.sourceID, ra); uerr != nil {
								log.Printf("enrich: 429: удлинение cooldown по Retry-After: %v", uerr)
							} else {
								log.Printf("enrich: cooldown по Retry-After удлинен до %s", ra.Format("2006-01-02 15:04"))
							}
						}
					}
				}
				return rep, nil
			}
			return rep, err // только ctx-ошибки; прочие считаются внутри
		}
		if (i+1)%100 == 0 {
			log.Printf("enrich: %d/%d (записано %d, ошибок %d)",
				i+1, len(cands), rep.Done, rep.Failed)
		}
	}
	return rep, nil
}

// sourceInCooldown — конец активного cooldown источника (ТЗ §10.4, D13).
// Источник не в cooldown либо кулдаун истёк (RecoverCooldowns ещё не
// отработал) — нулевое время.
func sourceInCooldown(ctx context.Context, pool *pgxpool.Pool, sourceID string) (time.Time, error) {
	var until *time.Time
	err := pool.QueryRow(ctx,
		`SELECT cooldown_until FROM sources WHERE id = $1 AND state = 'cooldown'`,
		sourceID).Scan(&until)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if until == nil {
		return time.Time{}, nil
	}
	return *until, nil
}

// Классификация ответа на запрос контрольного объявления (C7).
const (
	controlAlive       = iota // страница получена — объявление живо
	controlDead               // 301/404/410/редирект — объявление исчезло
	controlRetry              // 403/5xx/сеть — неконклюзивно, повторить раз
	controlRateLimited        // 429 — стоп-сигнал всего прогона (D13)
)

// classifyControlFetch — результат fetch'а контрольного объявления
// теми же классами, что и пул (тот же bazos.DetailClient, та же
// семантика ошибок: 200 с живой страницей — жив; 301 на раздел/404/
// 410/редирект на другое объявление — исчез; 429 — жёсткий лимит).
func classifyControlFetch(err error) int {
	switch {
	case err == nil:
		return controlAlive
	case errors.Is(err, bazos.ErrRateLimited):
		return controlRateLimited
	case errors.Is(err, bazos.ErrListingGone), errors.Is(err, bazos.ErrListingRelisted):
		return controlDead
	default:
		return controlRetry
	}
}

// controlsVerdict — вердикт прогона по контрольным (C7).
// n == 0 — верифицировать нечем (fallback на гвард доли). Живы не
// меньше половины (alive*2 >= n) — сбор здоров, delisting по прямому
// свидетельству допустим без ограничения доли; меньшинство живых —
// отказ сбора (сбой сайта или смена формата URL бьёт по ВСЕМ
// контрольным сразу, в отличие от подлинного делистинга единичного).
func controlsVerdict(n, alive int) (ok, unverifiable bool) {
	if n == 0 {
		return false, true
	}
	return alive*2 >= n, false
}

// controlListing — контрольное объявление (id объекта + URL детали).
type controlListing struct {
	id  int64
	url string
}

// controlListings — n объявлений источника, заведомо живых по
// последнему успешному детальному fetch'у (C7): status active +
// detail_fetched_at IS NOT NULL (успешный fetch — последнее
// подтверждение живости) + fetch не старше 7 дней (контрольный
// обязан быть жив именно сейчас, а не на момент бэкапа); по
// объекту — последний (по fetched_at) линкинг.
//
// Набор СЛУЧАЙНЫЙ и перевыбирается каждый раунд (ORDER BY random()):
// удаление необратимо, и фиксированная пятёрка пропускает частичный
// отказ сбора — при ложно-отрицательной вероятности 20% пятёрка
// целиком проходит даже когда сбор уже сломан (P(5/5 живы | отказ)
// = 0.8^5 ≈ 33% раундов). 15 контрольных с перевыбором сокращают
// это до 0.8^15 ≈ 1% и исключают систематический провал конкретного
// подмножества объявлений. Запросы — тем же клиентом и тем же
// темпом (PageDelayMS), что и основной дренаж.
func controlListings(ctx context.Context, pool *pgxpool.Pool, sourceID, country string, n int) ([]controlListing, error) {
	q := `
WITH latest AS (
    SELECT DISTINCT ON (o.id) o.id, rl.source_url, o.detail_fetched_at
    FROM objects o
    JOIN object_listings ol ON ol.object_id = o.id
    JOIN raw_listings rl ON rl.id = ol.raw_listing_id
    WHERE o.status = 'active'
      AND o.detail_fetched_at IS NOT NULL
      AND o.detail_fetched_at >= now() - interval '7 days'
      AND ol.source_id = $1
      AND rl.source_url LIKE '%bazos.cz%'`
	var args []any
	args = append(args, sourceID)
	if country != "" {
		q += "\n      AND o.country = $" + fmt.Sprintf("%d", len(args)+1)
		args = append(args, country)
	}
	q += `
    ORDER BY o.id, rl.fetched_at DESC
)
SELECT id, source_url FROM latest
ORDER BY random()
LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, n)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("control listings: %w", err)
	}
	defer rows.Close()
	var out []controlListing
	for rows.Next() {
		var c controlListing
		if err := rows.Scan(&c.id, &c.url); err != nil {
			return nil, fmt.Errorf("control listings scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("control listings: %w", err)
	}
	return out, nil
}

// checkControls — C7: проверка здоровья сбора ДО применения
// делистинга по прямому свидетельству (301/404/410 при загрузке
// страницы объявления). Прямое свидетельство — другой класс сигнала,
// чем вывод по отсутствию (delist-пасс), и
// delist.max_delisted_share_pct на него не распространяется; вместо
// доли — контрольные:
//
//   - живы не меньше половины → unlimited: delisting без ограничения доли;
//   - меньшинство живых → CollectionFailed: прогон остановлен ДО пула,
//     delisting не применён, уведомление оператору (отказ сбора);
//   - контрольных нет → unlimited=false: fallback на гвард доли.
//
// Возвращает unlimited, источник 429 ("" — не было; Run переводит его
// в cooldown той же механикой, что 429 пула) и ошибку.
func checkControls(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, det *bazos.DetailClient, country string, srcIDs []string, rep *Report) (unlimited bool, rateSrc string, err error) {
	n := cfg.Delist.Controls
	if n <= 0 {
		return false, "", nil // fallback: гвард доли
	}
	delay := time.Duration(cfg.Detail.PageDelayMS) * time.Millisecond
	var deadDetail []string
	for i, src := range srcIDs {
		controls, err := controlListings(ctx, pool, src, country, n)
		if err != nil {
			return false, "", fmt.Errorf("enrich: контрольные %s: %w", src, err)
		}
		for j, ctl := range controls {
			if (i > 0 || j > 0) && delay > 0 {
				select {
				case <-ctx.Done():
					return false, "", ctx.Err()
				case <-time.After(delay):
				}
			}
			class := controlRetry
			for attempt := 0; attempt < 2; attempt++ {
				_, ferr := det.Fetch(ctx, ctl.url)
				class = classifyControlFetch(ferr)
				if class != controlRetry || attempt == 1 {
					break
				}
				// Неконклюзивно (403/5xx/сеть): пауза и повтор один раз.
				if delay > 0 {
					select {
					case <-ctx.Done():
						return false, "", ctx.Err()
					case <-time.After(delay):
					}
				}
			}
			rep.ControlsChecked++
			switch class {
			case controlAlive:
				rep.ControlsAlive++
			case controlDead:
				deadDetail = append(deadDetail, fmt.Sprintf("%s: объект %d", src, ctl.id))
				// Контрольное само могло подлинно исчезнуть — но
				// вердикт по большинству: единичный dead среди живых
				// не останавливает прогон.
				log.Printf("enrich: контрольный объект %d (%s) исчез — учтён как мёртвый", ctl.id, src)
			case controlRetry:
				// Двойной провал — консервативно считаем мёртвым:
				// здоровье сбора не верифицировано, применять
				// неверифицированный delisting нельзя.
				deadDetail = append(deadDetail, fmt.Sprintf("%s: объект %d (повторная ошибка fetch)", src, ctl.id))
				log.Printf("enrich: контрольный объект %d (%s): повторная ошибка fetch — учтён как мёртвый (консервативно)", ctl.id, src)
			case controlRateLimited:
				// 429 — жёсткий лимит (ТЗ §10.4, D13): источник в
				// cooldown той же механикой, что 429 пула; прогон
				// останавливается (Run по rateSrc).
				rep.RateLimited = true
				strikes, until, berr := schedule.ApplyBackoff(ctx, pool, schedule.SettingsFrom(cfg), src, time.Now())
				if berr != nil {
					log.Printf("enrich: 429 (контроль): запись cooldown: %v", berr)
				} else {
					log.Printf("enrich: HTTP 429 на контрольном — источник %q в cooldown до %s (срыв #%d), прогон остановлен (ТЗ §10.4)",
						src, until.Format("2006-01-02 15:04"), strikes)
				}
				return false, src, nil
			}
		}
	}
	ok, unverifiable := controlsVerdict(rep.ControlsChecked, rep.ControlsAlive)
	if !ok && !unverifiable {
		rep.CollectionFailed = true
		if nerr := insertCollectionFailureNotice(ctx, pool, rep.ControlsChecked, rep.ControlsAlive, deadDetail); nerr != nil {
			log.Printf("enrich: уведомление об отказе сбора не записано: %v", nerr)
		}
	}
	if ok {
		log.Printf("enrich: контрольные %d/%d живы — delisting по прямому свидетельству без ограничения доли",
			rep.ControlsAlive, rep.ControlsChecked)
	}
	return ok, "", nil
}

// applyDelisting — маркер delisted (ТЗ §8.2): status/причина/время.
// true — маркер применён (объект был active).
func applyDelisting(ctx context.Context, pool *pgxpool.Pool, id int64, reason string) (bool, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE objects
		SET status = 'delisted', delisted_reason = $2, delisted_at = now()
		WHERE id = $1 AND status = 'active'`, id, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// insertCollectionFailureNotice — уведомление оператора (C7):
// контрольные тоже исчезли = отказ сбора (сбой сайта или смена
// формата URL), прогон остановлен до обработки пула, delisting не
// применён; бот доставит, статус pending.
func insertCollectionFailureNotice(ctx context.Context, pool *pgxpool.Pool, checked, alive int, deadDetail []string) error {
	payload, err := json.Marshal(map[string]any{
		"controls_checked": checked,
		"controls_alive":   alive,
		"dead_sample":      deadDetail,
		"detail":           "enrich: контрольные объявления тоже вернули 301/404/редирект — отказ сбора (сбой источника или смена формата URL); прогон остановлен до обработки пула, delisting не применён (C7)",
	})
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO notifications (channel, recipient, kind, payload)
		VALUES ('telegram', 'operator', 'enrich_collection_failure', $1)`, payload)
	return err
}

// processObject — один объект: fetch → parse → derive → seller →
// geocode → запись в БД (транзакция). Fetch/parse/DB-ошибки
// считаются в rep и не прерывают прогон: объект остаётся
// необработанным и повторится следующим прогоном. Исключение —
// 429 (bazos.ErrRateLimited, ТЗ §10.4, D13): не ошибка объекта, а
// стоп-сигнал всего прогона, возвращается вызывающему.
//
// delistUnlimited (C7): здоровье сбора проверено контрольными
// объектами — делистинг по прямому свидетельству (301/404/410)
// применяется БЕЗ delist.max_delisted_share_pct (тот гвард — для
// вывода по отсутствию в delist-пассе). false — контрольной
// ссылки нет: консервативный fallback на гвард доли (ТЗ §8.2,
// защита №3).
func processObject(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, det *bazos.DetailClient, gc geocode.Geocoder, regKeys map[string]bool, eng *attrules.Engine, c candidate, delistUnlimited bool, rep *Report) error {
	page, err := det.Fetch(ctx, c.url)
	if err != nil {
		// 429 — жёсткий лимит источника: прогон останавливается,
		// источник уходит в cooldown (обработка — в Run).
		if errors.Is(err, bazos.ErrRateLimited) {
			return err
		}
		if reason := delistReason(err); reason != "" {
			// Объявление исчезло (ТЗ §8.2): делистинг подтверждён самой
			// площадкой — 301 на раздел/404/410 или редирект на другое
			// объявление. Объект выводится из active-пула (honest: сигнал
			// от источника, не догадка). Статус верифицирован —
			// единое основание гварда (D18).
			rep.Verified++
			if delistUnlimited {
				// C7: прямое свидетельство удаления — сбор верифицирован
				// контрольными, ограничение доли не распространяется.
				// Подтверждённое удаление — результат, не ошибка.
				applied, uerr := applyDelisting(ctx, pool, c.id, reason)
				if uerr != nil {
					rep.Failed++
					log.Printf("enrich: объект %d: delisted: %v", c.id, uerr)
					return nil
				}
				if applied {
					rep.Delisted++
					logDelisted(c.id, reason)
				}
				return nil
			}
			// Контрольной ссылки нет — аномалий-гвард (ТЗ §8.2,
			// защита №3, как в delist-пассе): массовое «исчезновение» —
			// это скорее смена формата URL или сбой сайта, а не
			// делистинг — маркер не применяем, уведомляем оператора,
			// объекты остаются active (консервативно).
			if !rep.DelistAnomaly &&
				delistGuardFires(rep.Delisted, rep.Verified, cfg.Delist.MaxDelistedSharePct, cfg.Delist.MinObjectsForShare) {
				rep.DelistAnomaly = true
				log.Printf("enrich: АНОМАЛИЯ: доля delisted %d/%d > %.0f%% — дальнейший delisting не применён, объекты остаются active (fallback без контрольных)",
					rep.Delisted+1, rep.Verified, cfg.Delist.MaxDelistedSharePct)
				if nerr := insertDelistAnomalyNotice(ctx, pool, rep, cfg.Delist.MaxDelistedSharePct); nerr != nil {
					log.Printf("enrich: уведомление об аномалии delisting'а не записано: %v", nerr)
				}
			}
			if !rep.DelistAnomaly {
				applied, uerr := applyDelisting(ctx, pool, c.id, reason)
				if uerr != nil {
					rep.Failed++
					log.Printf("enrich: объект %d: delisted: %v", c.id, uerr)
					return nil
				}
				if applied {
					rep.Delisted++
					logDelisted(c.id, reason)
				}
				return nil
			}
			// Гвард сработал: сигнал delisting'а не применён, объект
			// остаётся active и повторится следующим прогоном.
			rep.Failed++
			log.Printf("enrich: объект %d: сигнал delisting'а не применён (гвард аномалии) — повторится: %v", c.id, err)
			return nil
		}
		rep.Failed++
		log.Printf("enrich: объект %d: детальная страница: %v", c.id, err)
		return nil
	}
	rep.Verified++ // fetch завершился страницей — объявление живо (D18)
	d, err := bazos.ParseDetail(page)
	if err != nil {
		// Изменение верстки или 404: честно не отмечаем обработанным.
		rep.Failed++
		log.Printf("enrich: объект %d: разбор: %v", c.id, err)
		return nil
	}

	detailHash := sha256Hex(page)
	der := bazos.Derive(d, bazos.RuleConfidence{
		Title:       cfg.Detail.RuleConfidenceTitle,
		Description: cfg.Detail.RuleConfidenceDescription,
	})
	sellerType, sellerConf := classifySeller(cfg.SellerRules, d.SellerName, d.Description)

	// Геокодирование (ТЗ v4 §4.5, C10): PSČ-центроид площадки не финал;
	// улица из текста → Nominatim, если уровень площадки ниже refine_below.
	var src *geocode.Point
	zoom := 0
	if d.SourceLat != nil && d.SourceLng != nil {
		src = &geocode.Point{Lat: *d.SourceLat, Lng: *d.SourceLng}
		if d.SourceZoom != nil {
			zoom = *d.SourceZoom
		}
	}
	street := der.Street
	if street == "" {
		loc := ""
		if d.Block != nil {
			loc = d.Block["Lokalita"]
		}
		street = geocode.ExtractStreet(d.Description+"\n"+d.Title+"\n"+loc, d.City)
	}
	gq := geocode.Query{
		Street: street, City: d.City, Postcode: d.Postcode,
		Country: countryName(c.country), CountryCode: c.country,
	}
	geo, gerr := geocode.Resolve(ctx, cfg.Geocode, src, "", zoom, gq, gc)
	if gerr != nil {
		// Ошибка провайдера (сеть/лимит): координаты площадки, если
		// они есть, сохраняем без уровня (честный fallback); без них —
		// объект остаётся без точек. Детальные данные не теряем.
		rep.GeocodeErr++
		log.Printf("enrich: объект %d: геокодер: %v", c.id, gerr)
		geo = nil
		if src != nil {
			geo = &geocode.Result{Lat: src.Lat, Lng: src.Lng, Source: "source"}
		}
	}
	if geo != nil {
		rep.Geocoded++
	}

	if err := saveObject(ctx, pool, cfg, c, detailHash, d, der, sellerType, sellerConf, street, geo, regKeys, eng, rep); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rep.Failed++
		log.Printf("enrich: объект %d: запись: %v", c.id, err)
		return nil
	}
	rep.Done++
	return nil
}

// delistReason — причина делистинга по ошибке детального fetch'а
// (ТЗ §8.2, та же классификация, что urlCheck delist-пасса):
// объявления больше нет → 'unknown'; редирект на другое объявление
// того же домена → 'relisted'; прочие ошибки (403/429/5xx, сеть,
// layout change) → "" — не делистинг, объект повторится.
// 'withdrawn_by_owner'/'scan_gap' автоматикой не присваиваются
// (зарезервированы за оператором, как в delist-пассе).
func delistReason(err error) string {
	switch {
	case errors.Is(err, bazos.ErrListingRelisted):
		return "relisted"
	case errors.Is(err, bazos.ErrListingGone):
		return "unknown"
	}
	return ""
}

// logDelisted — строка лога о применении delisted (ТЗ §8.2).
func logDelisted(id int64, reason string) {
	what := "исчезло"
	if reason == "relisted" {
		what = "перезалищено (редирект на другое объявление)"
	}
	log.Printf("enrich: объект %d: объявление %s → delisted (reason=%s)", id, what, reason)
}

// delistGuardFires — аномалий-гвард (ТЗ §8.2, защита №3; D18).
// Единое основание: delisted и доля считаются от одного множества —
// объектов, верифицированных в раунде (verified). +1 в сравнении
// проективный (считает текущий объект delisted) — только внутренний,
// в пейлоад никогда не попадает. Доля не вычисляется при
// verified < minObjects: на малом раунде большая доля не является
// сигналом сбоя источника, «0 из 1 (100.0%)» невозможно.
func delistGuardFires(delisted, verified int, maxSharePct float64, minObjects int) bool {
	if verified == 0 || verified < minObjects {
		return false
	}
	return 100*float64(delisted+1)/float64(verified) > maxSharePct
}

// insertDelistAnomalyNotice — уведомление оператора в очередь
// notifications (аналог insertAnomalyNotice delist-пасса, ТЗ §8.2
// защита №3): бот доставит, статус pending. D18: delisted и share_pct
// — от одного множества, верифицированных в раунде (rep.Verified);
// проективного +1 в пейлоаде нет.
func insertDelistAnomalyNotice(ctx context.Context, pool *pgxpool.Pool, rep *Report, maxSharePct float64) error {
	payload, err := json.Marshal(map[string]any{
		"verified":      rep.Verified,
		"delisted":      rep.Delisted,
		"share_pct":     100 * float64(rep.Delisted) / float64(rep.Verified),
		"max_share_pct": maxSharePct,
		"detail":        "enrich: доля delisted-объектов превышает порог (ТЗ §8.2) — возможна смена формата URL или сбой источника; дальнейший delisting не применён, объекты остаются active",
	})
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO notifications (channel, recipient, kind, payload)
		VALUES ('telegram', 'operator', 'enrich_delist_anomaly', $1)`, payload)
	return err
}

// saveObject — запись объекта в одной транзакции:
// objects (деталь, атрибуты, продавец, координаты), attribute_values
// (provenance), object_photos (оригинальные URL), price_history
// (контроль цены).
func saveObject(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, c candidate, detailHash string, d *bazos.Detail, der bazos.Derived, sellerType string, sellerConf float64, street string, geo *geocode.Result, regKeys map[string]bool, eng *attrules.Engine, rep *Report) error {
	// Выведенные атрибуты → ключи реестра (ТЗ v4 §4.1).
	rows := []struct {
		key string
		a   *bazos.Attr
	}{
		{"area_sqm", der.Area},
		{"total_area", der.TotalArea},
		{"rooms", der.Rooms},
		{"floor", der.Floor},
		{"floors", der.Floors},
		{"building_material", der.Material},
		{"energy_class", der.EnergyClass},
		{"ownership_type", der.Ownership},
		{"condition", der.Condition},
		{"has_elevator", der.HasElevator},
		{"new_build", der.NewBuild},
		{"year_built", der.YearBuilt},
		{"monthly_expenses", der.MonthlyCZK},
		{"plot_area", der.PlotArea},
	}
	if sellerType != "" {
		rows = append(rows, struct {
			key string
			a   *bazos.Attr
		}{"seller_type", &bazos.Attr{Value: sellerType, Method: "rule_markup", Confidence: sellerConf}})
	}

	// Теневой контроль (C8): хиты ПРОМОУТНУТЫХ шаблонов уходят в очередь
	// после commit записи объекта (объект, не получивший значение, в тень
	// не идёт); seed-шаблоны в тень не идут — promoted=false.
	var shadowHits []attrules.Hit

	// Шаблонный слой (ТЗ v4 §4.2, гибридное извлечение): атрибуты
	// реестра, не покрытые Derive()/продавцом. Заголовок раньше описания;
	// первый хит на атрибут выигрывает (порядок шаблонов = id;
	// гварды-отрицания ставятся раньше утверждений). Зеркало: area_sqm —
	// при пустом Derive() шаблонный хит заполняет и колонку (COALESCE
	// ниже: markup-значение при появлении всегда перетирает); rooms —
	// исключён (числа комнат из текста не берём).
	if eng != nil && eng.Len() > 0 {
		covered := map[string]bool{}
		for _, r := range rows {
			if r.a != nil {
				covered[r.key] = true
			}
		}
		missing := map[string]bool{}
		for k := range regKeys {
			if covered[k] {
				continue
			}
			if attrules.MirrorKeys[k] && k != "area_sqm" {
				continue
			}
			missing[k] = true
		}
		if len(missing) > 0 {
			for k, h := range eng.Apply(d.Title, d.Description, missing) {
				rows = append(rows, struct {
					key string
					a   *bazos.Attr
				}{k, &bazos.Attr{Value: h.Value, Method: "rule_text", Confidence: h.Confidence}})
				if h.Promoted {
					shadowHits = append(shadowHits, h)
				}
			}
		}
	}

	// Разделение по реестру страны (то же правило, что в сканере):
	// в реестре → attributes (читает модель оценки), вне —
	// attributes_unmapped.
	attrs := map[string]any{}
	unmapped := map[string]any{}
	var priced []struct {
		key    string
		method string
		conf   float64
	}
	for _, r := range rows {
		if r.a == nil {
			continue
		}
		if regKeys[r.key] {
			attrs[r.key] = r.a.Value
			priced = append(priced, struct {
				key    string
				method string
				conf   float64
			}{r.key, r.a.Method, r.a.Confidence})
		} else {
			unmapped[r.key] = r.a.Value
		}
	}
	rep.Attrs += len(priced)
	if sellerType != "" {
		rep.SellerType++
	}
	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("attributes json: %w", err)
	}
	unmappedJSON, err := json.Marshal(unmapped)
	if err != nil {
		return fmt.Errorf("attributes_unmapped json: %w", err)
	}

	// Зеркала колонок: area_sqm/rooms (NUMERIC/SMALLINT).
	var areaCol *float64
	if der.Area != nil {
		if f, ok := der.Area.Value.(float64); ok {
			areaCol = &f
		}
	} else {
		// C6: Derive() не дал площади — шаблонный хит (rule_text)
		// заполняет зеркало; COALESCE в UPDATE: при появлении
		// markup-значения деталь всегда перетирает.
		for _, r := range rows {
			if r.key == "area_sqm" && r.a != nil {
				if f, ok := r.a.Value.(float64); ok {
					areaCol = &f
				}
				break
			}
		}
	}
	var roomsCol *int
	if der.Rooms != nil {
		if n, ok := der.Rooms.Value.(int); ok {
			roomsCol = &n
		}
	}

	// Зеркало seller_type (D15): markup не нашёл продавца — шаблонный
	// хит (rule_text) заполняет пустую колонку, то же правило, что
	// area_sqm (C6): COALESCE в UPDATE — существующее значение не
	// переписывается, markup при появлении всегда в приоритете.
	var sellerCol *string
	if sellerType != "" {
		sellerCol = &sellerType
	} else {
		for _, r := range rows {
			if r.key == "seller_type" && r.a != nil {
				if s, ok := r.a.Value.(string); ok {
					sellerCol = &s
				}
				break
			}
		}
	}

	// Адрес: улица (деталь) точнее адреса категории (город + PSČ),
	// поэтому перекрываем только когда улица найдена.
	var addr string
	if street != "" {
		if d.City != "" {
			addr = street + ", " + d.City
		} else {
			addr = street
		}
		if d.Postcode != "" {
			addr += ", " + d.Postcode
		}
	}

	// Координаты: ST_MakePoint(longitude, latitude).
	var geoLng, geoLat *float64
	var geoConf, geoSrc *string
	if geo != nil {
		lng, lat := geo.Lng, geo.Lat
		geoLng, geoLat = &lng, &lat
		geoSrc = &geo.Source
		if geo.Confidence != "" {
			geoConf = &geo.Confidence
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // после Commit — no-op

	if _, err := tx.Exec(ctx, `
UPDATE objects SET
    detail_fetched_at = now(),
    detail_hash = $2,
    description_original = COALESCE($3, description_original),
    area_sqm = COALESCE($4, area_sqm),
    rooms = COALESCE($5, rooms),
    address = COALESCE(NULLIF($6, ''), address),
    attributes = attributes || $7::jsonb,
    attributes_unmapped = attributes_unmapped || $8::jsonb,
    seller_type = COALESCE($9, seller_type),
    seller_name = COALESCE(NULLIF($10, ''), seller_name),
    geom = CASE WHEN $11::float8 IS NOT NULL
               THEN ST_SetSRID(ST_MakePoint($11::float8, $12::float8), 4326)::geography
               ELSE geom END,
    geo_confidence = COALESCE($13, geo_confidence),
    geo_source = COALESCE($14, geo_source),
    -- C9: координаты изменились → привязка к зоне требует повторной
    -- проверки (инкрементальный assign; те же координаты — метка не
    -- трогается, повторная привязка не нужна).
    zone_dirty = CASE WHEN $11::float8 IS NOT NULL
                       AND geom IS DISTINCT FROM ST_SetSRID(ST_MakePoint($11::float8, $12::float8), 4326)::geography
                      THEN TRUE ELSE zone_dirty END,
    -- C20: те же координаты — терминальная метка не трогается; новые —
    -- снимается (терминальность выведена от точки; новая точка
    -- возвращает объект в рабочее множество assign).
    zone_terminal = CASE WHEN $11::float8 IS NOT NULL
                          AND geom IS DISTINCT FROM ST_SetSRID(ST_MakePoint($11::float8, $12::float8), 4326)::geography
                         THEN FALSE ELSE zone_terminal END
WHERE id = $1`,
		c.id, detailHash, orNil(d.Description), areaCol, roomsCol, addr,
		attrsJSON, unmappedJSON, sellerCol, orNil(d.SellerName),
		geoLng, geoLat, geoConf, geoSrc,
	); err != nil {
		return fmt.Errorf("objects update: %w", err)
	}

	// Provenance значений (ТЗ v4 §4.2): метод, уверенность, хэш страницы.
	for _, p := range priced {
		if _, err := tx.Exec(ctx, `
INSERT INTO attribute_values (object_id, attr_key, extracted_by, confidence, source_hash, extracted_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (object_id, attr_key) DO UPDATE SET
    extracted_by = EXCLUDED.extracted_by,
    confidence = EXCLUDED.confidence,
    source_hash = EXCLUDED.source_hash,
    extracted_at = EXCLUDED.extracted_at`,
			c.id, p.key, p.method, p.conf, detailHash); err != nil {
			return fmt.Errorf("attribute_values %s: %w", p.key, err)
		}
	}

	// Фотографии: оригинальные URL в порядке площадки (локальные копии —
	// pb photos run). DELETE+INSERT: порядок и состав страницы — источник
	// истины на момент детали.
	photos := d.Photos
	if max := cfg.Detail.MaxPhotos; max > 0 && len(photos) > max {
		photos = photos[:max]
	}
	if len(photos) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM object_photos WHERE object_id = $1`, c.id); err != nil {
			return fmt.Errorf("object_photos delete: %w", err)
		}
		for i, u := range photos {
			if _, err := tx.Exec(ctx, `
INSERT INTO object_photos (object_id, ord, original_url)
VALUES ($1, $2, $3)`, c.id, i+1, u); err != nil {
				return fmt.Errorf("object_photos insert: %w", err)
			}
		}
		rep.Photos += len(photos)
	}

	// Контроль цены: цена на детали ≠ текущая цена объекта → событие
	// в price_history. current_price_minor не трогаем (за сканером).
	if d.PriceMinor != nil && c.priceMinor != nil && *d.PriceMinor != *c.priceMinor {
		cur := d.Currency
		if cur == "" && c.currency != nil {
			cur = *c.currency
		}
		if cur == "" {
			return fmt.Errorf("price_history: валюта неизвестна")
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO price_history (object_id, source_id, price_minor, currency, change_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (object_id, source_id, change_at) DO NOTHING`,
			c.id, c.sourceID, *d.PriceMinor, cur); err != nil {
			return fmt.Errorf("price_history: %w", err)
		}
		rep.PriceDiff++
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	// Запись объекта зафиксирована — теневой контроль (C8): доля
	// shadow_sample_pct (детерминированная выборка по id объекта).
	for _, h := range shadowHits {
		if _, err := attrules.Enqueue(ctx, pool, h.TemplateID, c.id, h.Key, h.Value, cfg.Rules.ShadowSamplePct); err != nil {
			return fmt.Errorf("enrich: объект %d: теневая очередь: %w", c.id, err)
		}
	}
	return nil
}

// orNil — "" → NULL (для COALESCE-сведений).
func orNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// classifySeller — тип продавца по маркерам (ТЗ v4 §4.7, шаг 2 цепочки
// «структурное поле → правило → LLM»).
//
// Порядок — от частного к общему:
//  1. private — имя ИЛИ описание: маркеры — прямые заявления
//     («jsem majitel», «bez realitky»), их не выдумывают;
//  2. developer — имя ИЛИ описание: «stavební společnost», «developer».
//     ДО agency: «stavební společnost» содержит agency-маркер
//     «společnost» — общее не должно перекрывать конкретное;
//  3. agency — только ИМЯ продавца: «realitní kanceláře nevolat»
//     (private-маркер в описании) содержит «realitní kancelář» —
//     поиск agency в описании пометил бы частного продавца
//     агентством.
//
// Маркеров нет → ("", 0) — NULL в базе, догадка запрещена (шаг 3 —
// LLM, отдельный прогон).
func classifySeller(sr config.SellerRulesConf, name, description string) (string, float64) {
	nameL := strings.ToLower(name)
	descL := strings.ToLower(description)
	has := func(haystack string, markers []string) bool {
		for _, m := range markers {
			if m != "" && strings.Contains(haystack, strings.ToLower(m)) {
				return true
			}
		}
		return false
	}
	if has(nameL, sr.PrivateMarkers) || has(descL, sr.PrivateMarkers) {
		return "private", sr.ConfidencePrivate
	}
	if has(nameL, sr.DeveloperMarkers) || has(descL, sr.DeveloperMarkers) {
		return "developer", sr.ConfidenceDeveloper
	}
	if has(nameL, sr.AgencyMarkers) {
		return "agency", sr.ConfidenceAgency
	}
	return "", 0
}

// sha256Hex — sha256 страницы в hex (detail_hash, idempotency).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

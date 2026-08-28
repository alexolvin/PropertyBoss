// delist.go — маркировка объектов delisted (этап 6, ТЗ §8.2).
//
// «Исчезновение из выдачи не является продажей» (ТЗ §8.2): статус
// delisted с причиной присваивается только по правилам ниже, со всеми
// четырьмя защитами ТЗ.
package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
)

// DelistReport — результат одного delist-пасса для одного источника.
type DelistReport struct {
	SourceID string

	// Активных объектов, привязанных к источнику (знаменатель доли).
	Active int

	// Кандидатов на delisted: объектов, которых нет в N и более
	// последовательных полных сканах (N — config delist.min_consecutive_misses).
	Candidates int

	// Сработала защита №3: доля кандидатов превышает
	// max_delisted_share_pct. Прогон аномален: изменения к НИОДНОМУ
	// объекту не применены, оператору записано уведомление (строка
	// notifications, этап 8 доставит).
	Anomaly bool
	// Доля кандидатов среди активных объектов источника, %.
	SharePct float64

	// Объектов, фактически помеченных delisted.
	Delisted int
	// URL-чек (защита №4): объявление живо (2xx по тому же URL) —
	// объект остаётся active.
	URLAlive int
	// URL-чек не завершён (сеть, 403/429/5xx) — объект остаётся active:
	// подтверждения исчезновения нет.
	URLFailed int
}

// missQuery — для каждого активного объекта, привязанного к источнику $1,
// количество последовательных ПОЛНЫХ сканов, в которых объект не найден
// (ТЗ §8.2).
//
// «Объект найден в прогоне R» — есть запись raw_listings (R, источник,
// external_id), где external_id принадлежит объекту (через
// object_listings). По raw_listing_id в object_listings смотреть нельзя:
// он указывает на ПЕРВОЕ matched-объявление, а последующие сканы создают
// новые raw_listings на тот же (object, source, external_id).
//
// Неполные прогоны (partial/failed, включая пустую выдачу) в счёт не
// идут вообще (защита №1): условие r.completeness = 'complete'.
//
// Счёт идёт по «домашней» конфигурации поиска объекта — конфигурации
// последнего полного прогона, где объект найден; если объект нигде не
// найден — конфигурации первой записи. У источника с несколькими
// конфигурациями (разные города/фильтры) отсутствие объекта в выдаче
// ЧУЖОЙ конфигурации не считается промахом: объект может просто не
// входить в её диапазон поиска.
const missQuery = `
SELECT o.id,
       (SELECT count(*) FROM scan_runs r
         WHERE r.source_id = $1
           AND r.completeness = 'complete'
           AND r.search_config_id = attr.cfg
           AND (f.last_found_at IS NULL OR r.started_at > f.last_found_at)
       ) AS misses
FROM objects o
CROSS JOIN LATERAL (
    SELECT COALESCE(
        (SELECT r2.search_config_id
           FROM scan_runs r2
           JOIN object_listings ol2
             ON ol2.object_id = o.id AND ol2.source_id = $1
           JOIN raw_listings rl2
             ON rl2.scan_run_id = r2.id AND rl2.source_id = $1
            AND rl2.external_id = ol2.external_id
          WHERE r2.source_id = $1 AND r2.completeness = 'complete'
          ORDER BY r2.started_at DESC
          LIMIT 1),
        (SELECT r3.search_config_id
           FROM object_listings ol3
           JOIN raw_listings rl3 ON rl3.id = ol3.raw_listing_id
           JOIN scan_runs r3 ON r3.id = rl3.scan_run_id
          WHERE ol3.object_id = o.id AND ol3.source_id = $1
          ORDER BY rl3.fetched_at
          LIMIT 1)
    ) AS cfg
) attr
LEFT JOIN LATERAL (
    SELECT max(r4.started_at) AS last_found_at
      FROM scan_runs r4
      JOIN object_listings ol4
        ON ol4.object_id = o.id AND ol4.source_id = $1
      JOIN raw_listings rl4
        ON rl4.scan_run_id = r4.id AND rl4.source_id = $1
       AND rl4.external_id = ol4.external_id
     WHERE r4.source_id = $1 AND r4.completeness = 'complete'
) f ON true
WHERE o.status = 'active'
  AND attr.cfg IS NOT NULL
  AND EXISTS (SELECT 1 FROM object_listings ol
               WHERE ol.object_id = o.id AND ol.source_id = $1)`

// RunDelistPass — delist-пасс по одному источнику (ТЗ §8.2).
//
// Защита №1 (полные сканы) и №2 (N последовательных промахов, N >= 2)
// реализованы в missQuery и валидации конфига. Остальные:
//   - №3: если доля кандидатов > max_delisted_share_pct — прогон
//     аномален, изменения не применяются вообще, запись уведомления
//     оператору в notifications;
//   - №4: если источник разрешает (sources.url_check_allowed), перед
//     delisted объявление проверяется прямым URL-запросом: 404/410 —
//     исчезновение подтверждено; 2xx по тому же URL — объявление живо,
//     объект остаётся active; редирект на другое объявление того же
//     домена — причина 'relisted'.
//
// Автоматически присваиваются только причины 'unknown' и 'relisted'.
// 'sold' автоматикой НЕ присваивается никогда (ТЗ §8.2: подтверждает
// только кадастровый источник, их в системе нет). 'withdrawn_by_owner'
// и 'scan_gap' зарезервированы за оператором (ручная разметка).
func RunDelistPass(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, sourceID string) (*DelistReport, error) {
	rep := &DelistReport{SourceID: sourceID}

	var urlOK bool
	if err := pool.QueryRow(ctx, `SELECT url_check_allowed FROM sources WHERE id = $1`, sourceID).Scan(&urlOK); err != nil {
		return nil, fmt.Errorf("delist: источник %s: %w", sourceID, err)
	}

	rows, err := pool.Query(ctx, missQuery, sourceID)
	if err != nil {
		return nil, fmt.Errorf("delist: %s: подсчёт промахов: %w", sourceID, err)
	}
	type cand struct {
		id     int64
		misses int
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.misses); err != nil {
			rows.Close()
			return nil, fmt.Errorf("delist: %s: чтение строк: %w", sourceID, err)
		}
		rep.Active++
		if c.misses >= cfg.Delist.MinConsecutiveMisses {
			cands = append(cands, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delist: %s: чтение строк: %w", sourceID, err)
	}
	if rep.Active == 0 {
		return rep, nil
	}
	rep.Candidates = len(cands)
	if len(cands) > 0 {
		rep.SharePct = 100 * float64(len(cands)) / float64(rep.Active)
	}

	// Защита №3 — ДО любых изменений и URL-чеков: массовое исчезновение
	// — аномалия (смена вёрстки сайта «продаёт» всю базу), а не
	// делистинг.
	if rep.SharePct > cfg.Delist.MaxDelistedSharePct {
		rep.Anomaly = true
		if err := insertAnomalyNotice(ctx, pool, sourceID, rep, cfg.Delist.MaxDelistedSharePct); err != nil {
			return nil, fmt.Errorf("delist: %s: АНОМАЛИЯ подтверждена, но уведомление оператора не записано: %w", sourceID, err)
		}
		return rep, nil
	}

	// Защита №4 — URL-чек кандидатов, только если источник разрешает.
	// Последовательно, с вежливой паузой сканера между запросами.
	decisions := make([]string, len(cands)) // "unknown" | "relisted" | "" (оставить)
	client := &http.Client{
		Timeout: time.Duration(cfg.Delist.URLCheckTimeoutSec) * time.Second,
		// До 5 редиректов; конечный URL сравнивается с исходным
		// (редирект на другое объявление — сценарий 'relisted', ТЗ §8.2).
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	delay := cfg.ScanPageDelay()
	for i, c := range cands {
		if !urlOK {
			decisions[i] = "unknown"
			continue
		}
		if i > 0 && delay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		d, err := urlCheck(ctx, client, pool, c.id, sourceID)
		if err != nil {
			// Чек не завершён — подтверждения исчезновения нет:
			// объект остаётся active (честный исход, счётчик url_failed).
			rep.URLFailed++
			continue
		}
		decisions[i] = d
		if d == "" {
			rep.URLAlive++
		}
	}

	// Применение — один транзакция: либо все кандидаты, либо ничего.
	// WHERE status='active' — защита от гонки с параллельным сканом.
	var toDelist int
	for _, d := range decisions {
		if d != "" {
			toDelist++
		}
	}
	if toDelist == 0 {
		return rep, nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("delist: %s: транзакция: %w", sourceID, err)
	}
	for i, c := range cands {
		if decisions[i] == "" {
			continue
		}
		tag, err := tx.Exec(ctx, `
			UPDATE objects
			SET status = 'delisted', delisted_reason = $2, delisted_at = now()
			WHERE id = $1 AND status = 'active'`, c.id, decisions[i])
		if err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("delist: %s: обновление объекта %d: %w", sourceID, c.id, err)
		}
		if tag.RowsAffected() == 1 {
			rep.Delisted++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("delist: %s: коммит: %w", sourceID, err)
	}
	return rep, nil
}

// urlCheck — прямой URL-чек объявления кандидата (ТЗ §8.2, защита №4).
// Возвращает причину ('unknown' — объявления больше нет, 'relisted' —
// редирект на другое объявление того же домена) или пустую строку
// (объявление живо — delisted не присваивать). Ошибка — чек не
// завершён (сеть, таймаут, 403/429/5xx): решения нет.
func urlCheck(ctx context.Context, client *http.Client, pool *pgxpool.Pool, objID int64, sourceID string) (string, error) {
	var rawURL *string
	if err := pool.QueryRow(ctx, `
		SELECT rl.source_url
		  FROM raw_listings rl
		  JOIN object_listings ol ON ol.raw_listing_id = rl.id
		 WHERE ol.object_id = $1 AND ol.source_id = $2
		 ORDER BY rl.fetched_at DESC
		 LIMIT 1`, objID, sourceID).Scan(&rawURL); err != nil {
		return "", fmt.Errorf("последний URL объекта %d: %w", objID, err)
	}
	if rawURL == nil || *rawURL == "" {
		return "", errors.New("у объекта нет URL объявления")
	}
	orig, err := url.Parse(*rawURL)
	if err != nil {
		return "", fmt.Errorf("URL %q: %w", *rawURL, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "PropertyBoss-DelistCheck/1.0 (прямая проверка объявления, ТЗ §8.2)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// Тело не нужно, но вычитываем: вежливое закрытие соединения.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// 404/410 — исчезновение подтверждено (ТЗ §8.2: «повышает
		// уверенность»).
		return "unknown", nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		final := resp.Request.URL
		if final.Host == orig.Host && final.Path == orig.Path {
			return "", nil // объявление живо — скан его просто пропустил
		}
		if final.Host == orig.Host {
			// Редирект на другое объявление того же домена — объект
			// перезалистился (ТЗ §8.2: «редирект на похожее объявление
			// означает перезалист»).
			return "relisted", nil
		}
		return "", fmt.Errorf("редирект ушёл за пределы домена источника: %s → %s", orig.Host, final.Host)
	default:
		// 3xx после остановки редиректов, 403/429/5xx: подтверждения нет.
		return "", fmt.Errorf("неконлюзивный ответ %d", resp.StatusCode)
	}
}

// insertAnomalyNotice — уведомление оператора об аномальном прогоне
// (ТЗ §8.2, защита №3): строка в очереди notifications (канал telegram,
// статус pending) — бот этапа 8 доставит.
func insertAnomalyNotice(ctx context.Context, pool *pgxpool.Pool, sourceID string, rep *DelistReport, thresholdPct float64) error {
	payload, err := json.Marshal(map[string]any{
		"source_id":      sourceID,
		"active_objects": rep.Active,
		"candidates":     rep.Candidates,
		"share_pct":      rep.SharePct,
		"max_share_pct":  thresholdPct,
		"detail":         "delist-пасс остановлен: доля исчезнувших объектов превышает порог (ТЗ §8.2) — возможна смена вёрстки или сбой источника; изменения к объектам не применялись",
	})
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO notifications (channel, recipient, kind, payload)
		VALUES ('telegram', 'operator', 'delist_anomaly', $1)`, payload)
	return err
}

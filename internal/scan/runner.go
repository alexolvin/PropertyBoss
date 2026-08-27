package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
)

// Runner — запись результатов скана в БД: scan_runs → raw_listings →
// objects/price_history. Не знает о конкретных сайтах — только про
// Connector и схемы ТЗ §12.
type Runner struct {
	Pool *pgxpool.Pool
	// Dedupe — пороги сопоставления по странам (ТЗ §8.1, из конфига).
	Dedupe map[string]config.DedupeParams
}

// NewRunner — конструктор.
func NewRunner(pool *pgxpool.Pool, dedupe map[string]config.DedupeParams) *Runner {
	return &Runner{Pool: pool, Dedupe: dedupe}
}

// Report — итог одного скана (для лога и отчёта этапа).
type Report struct {
	RunID        int64
	SourceID     string
	Completeness string      // complete | partial | failed
	FailureKind  FailureKind // '' — нет
	Listings     int         // записано в raw_listings
	NewObjects   int
	Err          error // непусто, только если скан не состоялся (failed)
}

// Run — полный цикл скана: проверка источника (state/cooldown, ТЗ §13),
// scan_runs, коннектор, запись raw_listings, сопоставление с objects,
// завершение scan_runs с честной полнотой (ТЗ §8.2.1).
func (r *Runner) Run(ctx context.Context, sourceID string, conn Connector, cfg SearchConfig) *Report {
	rep := &Report{SourceID: sourceID, Completeness: "failed"}
	if conn.SourceID() != sourceID {
		rep.Err = fmt.Errorf("scan: коннектор %s не соответствует источнику %s", conn.SourceID(), sourceID)
		return rep
	}
	now := time.Now()

	// 1. Проверка источника.
	var (
		srcCountry string
		srcState   string
		cooldown   *time.Time
	)
	err := r.Pool.QueryRow(ctx,
		`SELECT country, state, cooldown_until FROM sources WHERE id = $1`, sourceID,
	).Scan(&srcCountry, &srcState, &cooldown)
	if errors.Is(err, pgx.ErrNoRows) {
		rep.Err = fmt.Errorf("scan: источник %q не найден в sources", sourceID)
		return rep
	}
	if err != nil {
		rep.Err = fmt.Errorf("scan: чтение источника %q: %w", sourceID, err)
		return rep
	}
	if srcState != "active" {
		rep.Err = fmt.Errorf("scan: источник %q в состоянии %q (ожидается active)", sourceID, srcState)
		return rep
	}
	if cooldown != nil && cooldown.After(now) {
		rep.Err = fmt.Errorf("scan: источник %q в кулдауне до %s", sourceID, cooldown.Format("2006-01-02 15:04"))
		return rep
	}
	if srcCountry != cfg.Country {
		rep.Err = fmt.Errorf("scan: страна источника %q (%s) != стране конфигурации (%s)", sourceID, srcCountry, cfg.Country)
		return rep
	}

	// 2. scan_runs: начало (completeness='running' до завершения).
	var runID int64
	if err := r.Pool.QueryRow(ctx,
		`INSERT INTO scan_runs (source_id, search_config_id, started_at, completeness)
		 VALUES ($1, $2, $3, 'running') RETURNING id`,
		sourceID, cfg.ID, now,
	).Scan(&runID); err != nil {
		rep.Err = fmt.Errorf("scan: создание scan_run: %w", err)
		return rep
	}
	rep.RunID = runID

	// 3. Коннектор.
	listings, issue, serr := conn.Scan(ctx, cfg)
	if serr != nil && len(listings) > 0 {
		// Контракт Connector: с err записи не возвращаются. Не доверяем —
		// данные отбрасываем, не записывая половинчатого скана.
		log.Printf("scan: run %d: коннектор вернул и ошибку, и %d записей — записи отброшены", runID, len(listings))
	}

	// 4. Полнота (ТЗ §8.2.1): complete — только непустая выдача без
	// ошибки/капчи; неполный скан не участвует в вычислении исчезновений.
	switch {
	case serr != nil:
		rep.Completeness = "failed"
		rep.FailureKind = Classify(serr)
		rep.Err = serr
		log.Printf("scan: run %d (%s) — failed: %v", runID, sourceID, serr)
	case issue != nil:
		rep.Completeness = "partial"
		rep.FailureKind = issue.Kind
		log.Printf("scan: run %d (%s) — partial: %s: %s", runID, sourceID, issue.Kind, issue.Message)
	case len(listings) == 0:
		// Пустая выдача не отличима от сбоя, который не дал ни строчки —
		// complete не присваиваем.
		rep.Completeness = "partial"
		log.Printf("scan: run %d (%s) — пустая выдача, помечен partial", runID, sourceID)
	default:
		rep.Completeness = "complete"
		log.Printf("scan: run %d (%s) — complete, записей %d", runID, sourceID, len(listings))
	}

	// 5. Запись результатов (только без фатальной ошибки).
	if serr == nil {
		n, m, merr := r.persistAndMatch(ctx, sourceID, runID, cfg, listings, now)
		rep.Listings = n
		rep.NewObjects = m
		if merr != nil {
			rep.Completeness = "failed"
			rep.FailureKind = ""
			rep.Err = fmt.Errorf("scan: запись результатов: %w", merr)
			log.Printf("scan: run %d: %v", runID, rep.Err)
		}
	}

	// 6. Завершение scan_runs (терминальное состояние).
	var kind *string
	if rep.FailureKind != "" {
		k := string(rep.FailureKind)
		kind = &k
	}
	if _, ferr := r.Pool.Exec(ctx,
		`UPDATE scan_runs SET finished_at = now(), completeness = $2,
		 failure_kind = $3, listings_found = $4, new_objects = $5
		 WHERE id = $1`,
		runID, rep.Completeness, kind, rep.Listings, rep.NewObjects,
	); ferr != nil {
		log.Printf("scan: run %d: обновление scan_runs: %v", runID, ferr)
		if rep.Err == nil {
			rep.Err = ferr
		}
	}
	return rep
}

// persistAndMatch — одной транзакцией: raw_listings + сопоставление с
// objects + price_history. Всё или ничего: сбой БД не оставляет
// половинчатых связей. Возвращает (записано, новых объектов).
func (r *Runner) persistAndMatch(ctx context.Context, sourceID string, runID int64, cfg SearchConfig, listings []Listing, now time.Time) (int, int, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op после Commit

	known, err := registryKeys(ctx, tx, cfg.Country)
	if err != nil {
		return 0, 0, fmt.Errorf("attribute_registry: %w", err)
	}

	// Дедупликация в рамках скана: (source_id, external_id) — идентичность.
	seen := make(map[string]bool, len(listings))
	var unique []Listing
	for _, l := range listings {
		if l.ExternalID == "" {
			log.Printf("scan: run %d: запись без external_id (url=%s) пропущена", runID, l.URL)
			continue
		}
		if seen[l.ExternalID] {
			log.Printf("scan: run %d: дубликат external_id %s в одном скане — оставлена первая", runID, l.ExternalID)
			continue
		}
		seen[l.ExternalID] = true
		unique = append(unique, l)
	}

	newObjects := 0
	for _, l := range unique {
		rawID, err := insertRawListing(ctx, tx, sourceID, runID, l, now)
		if err != nil {
			return 0, 0, fmt.Errorf("raw_listings[%s]: %w", l.ExternalID, err)
		}
		isNew, err := r.matchListing(ctx, tx, sourceID, cfg, l, rawID, known, now)
		if err != nil {
			return 0, 0, fmt.Errorf("match[%s]: %w", l.ExternalID, err)
		}
		if isNew {
			newObjects++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return len(unique), newObjects, nil
}

// registryKeys — ключи реестра атрибутов страны (ТЗ §6): для разделения
// objects.attributes и objects.attributes_unmapped.
func registryKeys(ctx context.Context, tx pgx.Tx, country string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT key FROM attribute_registry WHERE country = $1`, country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys[k] = true
	}
	return keys, rows.Err()
}

// insertRawListing — неизменяемое наблюдение (raw_listings).
func insertRawListing(ctx context.Context, tx pgx.Tx, sourceID string, runID int64, l Listing, now time.Time) (int64, error) {
	attrs, err := json.Marshal(l.Attributes)
	if err != nil {
		return 0, fmt.Errorf("attributes: %w", err)
	}
	if l.Attributes == nil {
		attrs = []byte("{}")
	}
	var rawID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO raw_listings (scan_run_id, source_id, external_id, source_url, fetched_at,
			price_minor, currency, area_sqm, rooms, property_type, geom, address, posted_at,
			attributes, description_original)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			CASE WHEN $11::text IS NULL THEN NULL ELSE ST_GeogFromText($11) END,
			$12, $13, $14, $15)
		RETURNING id`,
		runID, sourceID, l.ExternalID, l.URL, now,
		l.PriceMinor, l.Currency, l.AreaSqM, l.Rooms, l.PropertyType,
		ewkt(l.Lat, l.Lng), l.Address, l.PostedAt, attrs, l.Description,
	).Scan(&rawID)
	if err != nil {
		return 0, err
	}
	return rawID, nil
}

// ewkt — WKT-точка для ST_GeogFromText; nil, если координат нет.
func ewkt(lat, lng *float64) *string {
	if lat == nil || lng == nil {
		return nil
	}
	s := fmt.Sprintf("SRID=4326;POINT(%s %s)",
		strconv.FormatFloat(*lng, 'f', -1, 64),
		strconv.FormatFloat(*lat, 'f', -1, 64))
	return &s
}

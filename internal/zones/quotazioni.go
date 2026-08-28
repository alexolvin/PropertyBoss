package zones

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Quotazioni — импорт полугодовых котировок (OMI) в zone_reference_prices.
//
// Формат входного CSV (первая строка — имена колонок).
// Обязательные колонки:
//
//	codzona    — код зоны OMI; должен совпадать с zones.external_code страны
//	tipo       — класс объекта OMI; пишется в property_type как есть
//	             (crosswalk на каноническую таксономию — ответственность
//	             потребителя, этапа 5; см. отчёт этапа 4)
//	contratto  — vendita | affitto
//	prezzo_min — EUR/м², десятичная точка; пустая ячейка — NULL
//	prezzo_max — EUR/м², десятичная точка; пустая ячейка — NULL
//	periodo    — YYYY/1, YYYY/2, YYYY-S1, YYYY-S2, YYYY-H1, YYYY-H2
//	             (полугодие; period_start = 01.01 или 01.07)
//
// Необязательная колонка: nomezona — используется в текстах ошибок.
// Любая другая колонка — явная ошибка (молчаливо игнорировать нельзя, ТЗ §0.2).
// Разделитель — «,» или «;» (по строке заголовка).
// data_kind = 'transaction': котировки OMI строятся на сделках (ТЗ §7.1).
//
// ВАЖНО: формат зафиксирован по документации OMI, а не по реальному файлу —
// реальные выгрузки «Forniture dati OMI» выдаются только пользователям
// Fisconline/Entratel (ТЗ §7.1). Сверка с реальным файлом — пункт
// «требует ручной проверки» отчёта этапа 4.
const (
	QuotSource   = "Agenzia Entrate - OMI"
	quotCurrency = "EUR"
)

var (
	quotRequiredHeaders = []string{"codzona", "tipo", "contratto", "prezzo_min", "prezzo_max", "periodo"}
	quotHeaders         = append([]string{}, quotRequiredHeaders...)
)

func init() {
	quotHeaders = append(quotHeaders, "nomezona")
}

// quotRow — строка котировок, прошедшая валидацию.
type quotRow struct {
	zoneID int64
	deal   string
	ptype  string
	min    *int64
	max    *int64
	period time.Time
}

// quotKey — первичный ключ zone_reference_prices (для учёта created/updated).
type quotKey struct {
	zoneID int64
	deal   string
	ptype  string
	period time.Time
	source string
}

// QuotazioniReport — результат импорта котировок.
type QuotazioniReport struct {
	Rows              int
	Inserted          int
	Updated           int
	NullPrices        int // строк с отсутствующим min/max (NULL, честно)
	UnknownZones      int // строк с codzona вне zones
	UnknownZoneSample []string
}

// Quotazioni загружает котировки из CSV (документированный формат выше).
// Идемпотентно: повторный импорт того же файла обновляет строки (PK).
// Ошибки строк собираются (до 10) и возвращаются одной явной ошибкой;
// при ошибке ничего не записывается (транзакция).
func Quotazioni(ctx context.Context, pool *pgxpool.Pool, path, country, source string) (*QuotazioniReport, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 {
		return nil, fmt.Errorf("quotazioni: country: код из двух букв, получено %q", country)
	}
	if source == "" {
		source = QuotSource
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("quotazioni: чтение %s: %w", path, err)
	}
	text := string(raw)
	header := headerLine(text)
	r := csv.NewReader(strings.NewReader(text))
	if strings.Contains(header, ";") && !strings.Contains(header, ",") {
		r.Comma = ';'
	}
	r.TrimLeadingSpace = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("quotazioni: CSV: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("quotazioni: CSV пуст")
	}
	colIdx, err := quotColumns(records[0])
	if err != nil {
		return nil, err
	}
	nCols := len(records[0])

	// Валюта — из справочника, экспонента — не из кода (ТЗ §0, §5).
	var exponent int
	if err := pool.QueryRow(ctx, `SELECT exponent FROM currencies WHERE code = $1`,
		quotCurrency).Scan(&exponent); err != nil {
		return nil, fmt.Errorf("quotazioni: валюта %s отсутствует в справочнике currencies", quotCurrency)
	}

	// Зоны страны: codzona → id.
	zoneIDs := map[string]int64{}
	zrows, err := pool.Query(ctx,
		`SELECT external_code, id FROM zones WHERE country = $1 AND external_code IS NOT NULL`, country)
	if err != nil {
		return nil, fmt.Errorf("quotazioni: чтение зон: %w", err)
	}
	for zrows.Next() {
		var code string
		var id int64
		if err := zrows.Scan(&code, &id); err != nil {
			zrows.Close()
			return nil, err
		}
		zoneIDs[code] = id
	}
	zrows.Close()
	if err := zrows.Err(); err != nil {
		return nil, err
	}
	if len(zoneIDs) == 0 {
		return nil, fmt.Errorf("quotazioni: в zones нет ни одной зоны страны %s с external_code — импортируйте полигоны зон (pb zones import)", country)
	}

	// Существующие ключи — для учёта created/updated.
	existing := map[quotKey]bool{}
	erows, err := pool.Query(ctx, `
		SELECT zrp.zone_id, zrp.deal_type, zrp.property_type, zrp.period_start, zrp.source
		FROM zone_reference_prices zrp JOIN zones z ON z.id = zrp.zone_id
		WHERE z.country = $1`, country)
	if err != nil {
		return nil, fmt.Errorf("quotazioni: чтение существующих котировок: %w", err)
	}
	for erows.Next() {
		var k quotKey
		if err := erows.Scan(&k.zoneID, &k.deal, &k.ptype, &k.period, &k.source); err != nil {
			erows.Close()
			return nil, err
		}
		existing[k] = true
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return nil, err
	}

	rowErrs := []string{}
	addRowErr := func(line int, format string, args ...any) {
		if len(rowErrs) >= 10 {
			return
		}
		// Локальный тулчейн не компилирует f(format, line, args...) —
		// pass-through допустим только сразу после фиксированных параметров,
		// поэтому номер строки сначала дописываем в срез.
		all := append([]any{line}, args...)
		rowErrs = append(rowErrs, fmt.Sprintf("строка %d: "+format, all...))
	}
	rep := &QuotazioniReport{Rows: len(records) - 1}
	parsed := make([]quotRow, 0, len(records)-1)
	for i, rec := range records[1:] {
		line := i + 2 // номер строки в файле (1-индекс, с заголовком)
		if len(rec) != nCols {
			addRowErr(line, "колонок %d, ожидается %d", len(rec), nCols)
			continue
		}
		codzona := strings.TrimSpace(rec[colIdx["codzona"]])
		if codzona == "" {
			addRowErr(line, "codzona пуст")
			continue
		}
		zoneID, ok := zoneIDs[codzona]
		if !ok {
			rep.UnknownZones++
			addRowErr(line, "codzona %q не найден в zones (country=%s)%s", codzona, country,
				nomezonaSuffix(rec, colIdx))
			continue
		}
		ptype := strings.TrimSpace(rec[colIdx["tipo"]])
		if ptype == "" {
			addRowErr(line, "tipo пуст")
			continue
		}
		deal, err := quotDealType(rec[colIdx["contratto"]])
		if err != nil {
			addRowErr(line, "contratto %q: %v", rec[colIdx["contratto"]], err)
			continue
		}
		period, err := parseQuotPeriod(rec[colIdx["periodo"]])
		if err != nil {
			addRowErr(line, "periodo %q: %v", rec[colIdx["periodo"]], err)
			continue
		}
		minM, err := parseQuotPrice(rec[colIdx["prezzo_min"]], exponent)
		if err != nil {
			addRowErr(line, "prezzo_min: %v", err)
			continue
		}
		maxM, err := parseQuotPrice(rec[colIdx["prezzo_max"]], exponent)
		if err != nil {
			addRowErr(line, "prezzo_max: %v", err)
			continue
		}
		if minM != nil && maxM != nil && *maxM < *minM {
			addRowErr(line, "prezzo_max < prezzo_min")
			continue
		}
		parsed = append(parsed, quotRow{zoneID: zoneID, deal: deal, ptype: ptype, min: minM, max: maxM, period: period})
	}
	if len(rowErrs) > 0 {
		return nil, fmt.Errorf("quotazioni: ошибки в %d строках (первые: %s)", len(rowErrs), strings.Join(rowErrs, "; "))
	}

	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		for _, q := range parsed {
			if _, err := tx.Exec(ctx, `
				INSERT INTO zone_reference_prices
					(zone_id, deal_type, property_type, price_min_minor, price_max_minor,
					 currency, unit, period_start, data_kind, source)
				VALUES ($1, $2, $3, $4, $5, $6, 'per_sqm', $7, 'transaction', $8)
				ON CONFLICT (zone_id, deal_type, property_type, period_start, source)
				DO UPDATE SET price_min_minor = EXCLUDED.price_min_minor,
				             price_max_minor = EXCLUDED.price_max_minor`,
				q.zoneID, q.deal, q.ptype, q.min, q.max, quotCurrency, q.period, source); err != nil {
				return fmt.Errorf("quotazioni: запись (зона %d, %s, %s): %w", q.zoneID, q.deal, q.ptype, err)
			}
			if q.min == nil || q.max == nil {
				rep.NullPrices++
			}
			k := quotKey{q.zoneID, q.deal, q.ptype, q.period, source}
			if existing[k] {
				rep.Updated++
			} else {
				rep.Inserted++
				existing[k] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rep, nil
}

// quotColumns — строгая проверка заголовка: обязательные колонки на месте,
// лишних нет (неизвестная колонка — явная ошибка).
func quotColumns(header []string) (map[string]int, error) {
	known := map[string]bool{}
	for _, h := range quotHeaders {
		known[h] = true
	}
	seen := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			return nil, fmt.Errorf("quotazioni: пустое имя колонки в заголовке, позиция %d", i+1)
		}
		if !known[h] {
			return nil, fmt.Errorf("quotazioni: неизвестная колонка %q (допустимы: %s, nomezona)",
				h, strings.Join(quotRequiredHeaders, ", "))
		}
		if _, dup := seen[h]; dup {
			return nil, fmt.Errorf("quotazioni: колонка %q задана дважды", h)
		}
		seen[h] = i
	}
	for _, req := range quotRequiredHeaders {
		if _, ok := seen[req]; !ok {
			return nil, fmt.Errorf("quotazioni: в заголовке отсутствует обязательная колонка %q", req)
		}
	}
	return seen, nil
}

func quotDealType(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "vendita":
		return "sale", nil
	case "affitto":
		return "rent", nil
	default:
		return "", errors.New("допустимы vendita | affitto")
	}
}

// parseQuotPrice — точное преобразование цены в минорные единицы, без float (ТЗ §5).
// Пустая строка — NULL (честно: значения в файле нет).
func parseQuotPrice(s string, exponent int) (*int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" {
		return nil, fmt.Errorf("некорректное число %q", s)
	}
	if len(fracPart) > exponent {
		return nil, fmt.Errorf("%q: дробных знаков больше, чем экспонента валюты (%d) — округлять нельзя", s, exponent)
	}
	fracPart += strings.Repeat("0", exponent-len(fracPart))
	iv, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("некорректное число %q", s)
	}
	fv, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("некорректная дробная часть %q", s)
	}
	if iv < 0 || fv < 0 {
		return nil, errors.New("отрицательная цена")
	}
	v := iv*quotPow10(exponent) + fv
	return &v, nil
}

func quotPow10(n int) int64 {
	var v int64 = 1
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

// parseQuotPeriod — полугодие → дата начала периода (01.01 / 01.07).
func parseQuotPeriod(s string) (time.Time, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	var year, half string
	suffixOK := false
	for _, suf := range []string{"/1", "/2", "-s1", "-s2", "-h1", "-h2", "s1", "s2", "h1", "h2"} {
		if strings.HasSuffix(s, suf) {
			year, half, suffixOK = s[:len(s)-len(suf)], suf[len(suf)-1:], true
			break
		}
	}
	if !suffixOK {
		return time.Time{}, fmt.Errorf("формат YYYY/1|YYYY/2|YYYY-S1|YYYY-S2|YYYY-H1|YYYY-H2, получено %q", s)
	}
	y, err := strconv.Atoi(year)
	if err != nil || y < 1990 || y > 2100 {
		return time.Time{}, fmt.Errorf("некорректный год в %q", s)
	}
	switch half {
	case "1":
		return time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC), nil
	case "2":
		return time.Date(y, time.July, 1, 0, 0, 0, 0, time.UTC), nil
	default:
		return time.Time{}, fmt.Errorf("некорректное полугодие в %q", s)
	}
}

// nomezonaSuffix — для читаемых ошибок: имя зоны из необязательной колонки.
func nomezonaSuffix(rec []string, colIdx map[string]int) string {
	if i, ok := colIdx["nomezona"]; ok && i < len(rec) && strings.TrimSpace(rec[i]) != "" {
		return fmt.Sprintf(", zona %q", strings.TrimSpace(rec[i]))
	}
	return ""
}

// headerLine — первая строка файла (для выбора разделителя).
func headerLine(raw string) string {
	if i := strings.IndexByte(raw, '\n'); i >= 0 {
		return raw[:i]
	}
	return raw
}

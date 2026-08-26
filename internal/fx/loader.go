package fx

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Source — строка-метка источника в fx_rates.source (ТЗ §13: атрибуция).
const Source = "ECB eurofxref daily XML"

// FrankfurterSource — та же референсная статистика ЕЦБ, полученная через
// публичный зеркальный API (см. frankfurter.go).
const FrankfurterSource = "ECB eurofxref via frankfurter.dev"

// Load записывает курсы в fx_rates (base = EUR), апсертом по
// (base, quote, rate_date). Курс передаётся в БД строкой — NUMERIC
// Postgres разбирает его точно, без float.
//
// Записываются только валюты из справочника currencies; чужие коды из фида
// пропускаются и учитываются в skippedUnknown, а не отбрасываются молча (ТЗ §0.4).
func Load(ctx context.Context, pool *pgxpool.Pool, days []DayRates, source string) (loaded int, skippedUnknown int, err error) {
	var codes []string
	rows, err := pool.Query(ctx, "SELECT code FROM currencies")
	if err != nil {
		return 0, 0, fmt.Errorf("fx: чтение справочника валют: %w", err)
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return 0, 0, err
		}
		codes = append(codes, c)
	}
	rows.Close()
	known := make(map[string]bool, len(codes))
	for _, c := range codes {
		known[c] = true
	}

	for _, day := range days {
		date := day.Date.Format("2006-01-02")
		for _, cur := range sortedKeys(day.Rates) {
			if cur == "EUR" {
				continue // базовая валюта — в фиде её курс не публикуется
			}
			if !known[cur] {
				skippedUnknown++
				continue
			}
			tag, err := pool.Exec(ctx, `
				INSERT INTO fx_rates (base, quote, rate, rate_date, source)
				VALUES ('EUR', $1, $2, $3, $4)
				ON CONFLICT (base, quote, rate_date)
				DO UPDATE SET rate = EXCLUDED.rate, source = EXCLUDED.source`,
				cur, day.Rates[cur], date, source)
			if err != nil {
				return loaded, skippedUnknown, fmt.Errorf("fx: запись курса %s %s: %w", cur, date, err)
			}
			loaded += int(tag.RowsAffected())
		}
	}
	return loaded, skippedUnknown, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// SyncReport — результат загрузки по каналам. Ошибка конкретного канала
// сообщается явно (не «молча без данных»): вызывающий обязан её залогировать.
type SyncReport struct {
	Loaded         int
	SkippedUnknown int
	// Ошибка канала «прямой XML ЕЦБ» (последний опубликованный день).
	ECBErr error
	// Ошибка канала «frankfurter.dev, диапазон» (nil, если канал не задан).
	MirrorErr error
	MirrorUsed bool
}

// Sync — полный цикл для окна [сегодня-backfillDays, сегодня+1].
// Два канала, оба пишут в fx_rates с явной атрибуцией источника:
//  1. frankfurter.dev — диапазон дат (надёжно с этой сети, данные ЕЦБ);
//  2. прямой XML ЕЦБ — последний опубликованный день (официальный источник,
//     перезаписывает зеркало на эту дату).
//
// Ошибка одного канала не отменяет данные другого; ошибка обоих — ошибка
// вызывающего.
func Sync(ctx context.Context, ecb *ECBClient, mirror *FrankfurterClient, pool *pgxpool.Pool, backfillDays int) (*SyncReport, error) {
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -backfillDays)
	to := now.AddDate(0, 0, 1) // +1: на случай, что день уже опубликован
	rep := &SyncReport{}

	if mirror != nil {
		rep.MirrorUsed = true
		days, err := mirror.FetchRange(ctx, from, to)
		if err != nil {
			rep.MirrorErr = err
		} else {
			l, s, err := Load(ctx, pool, days, FrankfurterSource)
			rep.Loaded += l
			rep.SkippedUnknown += s
			if err != nil {
				rep.MirrorErr = err
			}
		}
	}

	ecbDays, err := ecb.FetchRange(ctx, from, to)
	if err != nil {
		rep.ECBErr = err
	} else {
		l, s, err := Load(ctx, pool, ecbDays, Source)
		rep.Loaded += l
		rep.SkippedUnknown += s
		if err != nil {
			rep.ECBErr = err
		}
	}

	if rep.ECBErr != nil && rep.MirrorErr != nil {
		return rep, fmt.Errorf("fx: оба канала недоступны: ecb: %v; mirror: %v", rep.ECBErr, rep.MirrorErr)
	}
	return rep, nil
}

package schedule

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BackoffDuration — длительность кулдауна за n-й ПОСЛЕДОВАТЕЛЬНЫЙ
// срыв (капча/429): base * multiplier^(n-1), с потолком max
// (ТЗ §10.4: «экспоненциальный откат»). Чистая функция — unit-тестируется.
func BackoffDuration(base time.Duration, multiplier float64, max time.Duration, strikes int) time.Duration {
	if strikes < 1 {
		strikes = 1
	}
	// Считаем в float64 и только потом переводим в Duration: при больших
	// strikes произведение переполнит int64 (time.Duration) и даст
	// отрицательный «кулдаун». Потолок max сравниваем ещё в float64.
	v := float64(base) * math.Pow(multiplier, float64(strikes-1))
	if v >= float64(max) {
		return max
	}
	return time.Duration(v)
}

// ApplyBackoff — капча/429/блокировка (ТЗ §10.4): источник немедленно
// переводится в cooldown, strikes +1, длительность — экспоненциальный
// откат, rate_factor падает вдвое (но не ниже min): частота снижается
// сразу и на два уровня — и по времени (кулдаун), и по плотности
// (потолок на час). Возвращает (новые strikes, конец кулдауна).
func ApplyBackoff(ctx context.Context, pool *pgxpool.Pool, s *Settings, sourceID string, now time.Time) (int, time.Time, error) {
	var strikes int
	err := pool.QueryRow(ctx,
		`SELECT cooldown_strikes FROM sources WHERE id = $1`, sourceID,
	).Scan(&strikes)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("schedule: чтение источника %q: %w", sourceID, err)
	}
	strikes++
	until := now.Add(BackoffDuration(s.BackoffBase, s.BackoffMultiplier, s.BackoffMax, strikes))
	_, err = pool.Exec(ctx, `
		UPDATE sources
		SET state = 'cooldown',
		    cooldown_strikes = $2,
		    cooldown_until = $3,
		    rate_factor = GREATEST($4, rate_factor / 2)
		WHERE id = $1`,
		sourceID, strikes, until, s.MinRateFactor)
	if err != nil {
		return 0, time.Time{}, err
	}
	return strikes, until, nil
}

// NoteSuccess — ПОЛНЫЙ успешный скан: лестница последовательных
// срывов сбрасывается (следующая капча откатывается от base, а не от
// вершины), rate_factor поднимается на один шаг (×recovery_step) к 1 —
// восстановление ПОСТЕПЕННОЕ, не скачком (ТЗ §10.4).
func NoteSuccess(ctx context.Context, pool *pgxpool.Pool, s *Settings, sourceID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE sources
		SET cooldown_strikes = 0,
		    rate_factor = LEAST(1.0, rate_factor * $2)
		WHERE id = $1`,
		sourceID, s.RecoveryStep)
	return err
}

// RecoverCooldowns — истёкшие кулдауны: источник возвращается в
// active. rate_factor при этом НЕ восстанавливается скачком — он
// растёт по шагам при последующих полных сканах (NoteSuccess,
// ТЗ §10.4: «Восстановление — постепенное»). Возвращает список
// вернувшихся источников (для лога). disabled/blocked (ручные
// состояния оператора) не трогаются.
func RecoverCooldowns(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]string, error) {
	rows, err := pool.Query(ctx, `
		UPDATE sources SET state = 'active'
		WHERE state = 'cooldown' AND cooldown_until IS NOT NULL AND cooldown_until <= $1
		RETURNING id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SourceState — состояние источника для планирования/отчёта.
type SourceState struct {
	ID            string
	Country       string
	State         string
	Strikes       int
	RateFactor    float64
	CooldownUntil *time.Time
}

// LoadSourceStates — все источники (для show и plan).
func LoadSourceStates(ctx context.Context, pool *pgxpool.Pool) ([]SourceState, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, country, state, cooldown_strikes, rate_factor, cooldown_until
		 FROM sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceState
	for rows.Next() {
		var ss SourceState
		if err := rows.Scan(&ss.ID, &ss.Country, &ss.State, &ss.Strikes, &ss.RateFactor, &ss.CooldownUntil); err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// EffectiveCap — эффективный потолок запросов на час:
// max_requests_per_hour × rate_factor, округлённый ВНИЗ
// (ТЗ §10.4: потолок, который адаптация не имеет права превысить).
func EffectiveCap(maxPerHour int, rateFactor float64) int {
	n := int(math.Floor(float64(maxPerHour) * rateFactor))
	if n < 0 {
		n = 0
	}
	return n
}

// ScansThisHour — сколько сканов источника началось в ТЕКУЩЕМ часовом
// слоте пояса страны (все completeness, включая running: и бегущий
// скан уже расходует запросы). Границы — в UTC, час — в поясе страны.
func ScansThisHour(ctx context.Context, pool *pgxpool.Pool, sourceID string, loc *time.Location, now time.Time) (int, error) {
	t := now.In(loc)
	hourStart := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc).UTC()
	var n int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM scan_runs
		WHERE source_id = $1 AND started_at >= $2 AND started_at < $3`,
		sourceID, hourStart, hourStart.Add(time.Hour),
	).Scan(&n)
	return n, err
}

package schedule

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RecordYield — учёт выхода скана (ТЗ §10.3): число новых объектов на
// скан по слоту (источник, час недели в поясе страны) и дню.
//
// Допущение исполнителя (ТЗ §0.2): в статистику попадают ТОЛЬКО полные
// (completeness='complete') прогоны. Неполный/сбойный скан не даёт
// информации о том, сколько новых объявлений появилось (та же логика,
// что и delisted-логика, ТЗ §8.2: partial/failed не участвуют), и
// засчитывать его как «0 новых» нечестно — это занизило бы слот.
// Complete-прогон с нулём новых — достоверная точка «в этот час новых
// объявлений не было» и записывается как есть.
func RecordYield(ctx context.Context, pool *pgxpool.Pool, sourceID string, loc *time.Location, startedAt time.Time, newObjects int) error {
	t := startedAt.In(loc)
	_, err := pool.Exec(ctx, `
		INSERT INTO scan_yield (source_id, slot_key, day, scans, new_objects)
		VALUES ($1, $2, $3, 1, $4)
		ON CONFLICT (source_id, slot_key, day)
		DO UPDATE SET scans = scan_yield.scans + 1,
		              new_objects = scan_yield.new_objects + EXCLUDED.new_objects`,
		sourceID, SlotKey(int(t.Weekday()), t.Hour()), t.Format("2006-01-02"), newObjects)
	if err != nil {
		return err
	}
	return nil
}

// SlotAgg — агрегированный выход по слоту за окно скользящего среднего.
type SlotAgg struct {
	Scans      int
	NewObjects int
}

// Avg — среднее новых объектов на скан (0, если сканов не было).
func (a SlotAgg) Avg() float64 {
	if a.Scans == 0 {
		return 0
	}
	return float64(a.NewObjects) / float64(a.Scans)
}

// TotalScans — всего полных сканов по источнику за всю историю
// scan_yield (для warming_up, ТЗ §10.5).
func TotalScans(ctx context.Context, pool *pgxpool.Pool, sourceID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT coalesce(sum(scans), 0) FROM scan_yield WHERE source_id = $1`, sourceID,
	).Scan(&n)
	return n, err
}

// SlotYields — выход по слотам за последние maDays дней (пояс страны —
// уже учтён при записи RecordYield; сравнение дат — по day, который
// тоже записан в поясе страны).
func SlotYields(ctx context.Context, pool *pgxpool.Pool, sourceID string, maDays int) (map[string]SlotAgg, error) {
	rows, err := pool.Query(ctx, `
		SELECT slot_key, sum(scans), sum(new_objects)
		FROM scan_yield
		WHERE source_id = $1 AND day >= (now()::date - $2::int)
		GROUP BY slot_key`, sourceID, maDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]SlotAgg)
	for rows.Next() {
		var (
			slot string
			a    SlotAgg
		)
		if err := rows.Scan(&slot, &a.Scans, &a.NewObjects); err != nil {
			return nil, err
		}
		out[slot] = a
	}
	return out, rows.Err()
}

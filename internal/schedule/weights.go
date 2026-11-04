package schedule

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Window — строка scan_windows с вычисленным весом.
type Window struct {
	ID         int64
	DOW        int
	HourStart  int
	HourEnd    int
	Timezone   string
	MaxPerHour int
	Weight     float64
	// SlotAvg — скользящее среднее выхода по слотам окна (nil, если за
	// окно наблюдений сканов не было: «нет данных» ≠ «выход 0»).
	SlotAvg   *float64
	SlotScans int
}

// hoursCovered — слоты окна (используется для агрегации выхода).
func (w *Window) slotKeys() []string {
	hs := WindowHours(w.HourStart, w.HourEnd)
	keys := make([]string, len(hs))
	for i, h := range hs {
		keys[i] = SlotKey(w.DOW, h)
	}
	return keys
}

// poolAvg — выход окна: новые объекты / сканы по ВСЕМ слотам окна
// (пул по слотам честнее среднего средних: слоты с большим числом
// сканов имеют больший голос). Сканы не были — (0, 0).
func (w *Window) poolAvg(yields map[string]SlotAgg) (float64, int) {
	var scans, fresh int
	for _, k := range w.slotKeys() {
		a, ok := yields[k]
		if !ok {
			continue
		}
		scans += a.Scans
		fresh += a.NewObjects
	}
	if scans == 0 {
		return 0, 0
	}
	return float64(fresh) / float64(scans), scans
}

// windowWeights — ЧИСТАЯ функция (unit-тестируется без БД):
// веса окон из выхода по слотам (ТЗ §10.3).
//
//   - warmingUp: равные веса 1.0 — консервативно, адаптация не
//     выдаётся за статистику (ТЗ §10.5);
//   - иначе weight = ε + (1−ε)·(avg/maxAvg): бюджет пропорционален
//     скользящему среднему выхода, а ε-пол гарантирует, что даже окно
//     с нулевым (или нулевым *наблюдаемым*) выходом получает долю
//     бюджета на исследование и не отключается насовсем (ТЗ §10.3).
//
// maxAvg = 0 (нигде данных нет) — все веса ε: тоже равно, тоже
// консервативно.
func windowWeights(windows []Window, yields map[string]SlotAgg, warmingUp bool, eps float64) []float64 {
	out := make([]float64, len(windows))
	if warmingUp {
		for i := range out {
			out[i] = 1.0
		}
		return out
	}
	avgs := make([]float64, len(windows))
	var maxAvg float64
	for i, w := range windows {
		avgs[i], _ = w.poolAvg(yields)
		if avgs[i] > maxAvg {
			maxAvg = avgs[i]
		}
	}
	for i := range windows {
		if maxAvg <= 0 {
			out[i] = eps
			continue
		}
		out[i] = eps + (1-eps)*(avgs[i]/maxAvg)
	}
	return out
}

// ComputeWeights — вычисляет веса окон источника по scan_yield и
// записывает их в scan_windows.weight (ТЗ §10.2: «weight
// вычисляемый, не назначаемый» — столбец хранит результат последнего
// расчёта для аудита). Возвращает окна с весами и флаг warming_up.
func ComputeWeights(ctx context.Context, pool *pgxpool.Pool, s *Settings, sourceID string) ([]Window, bool, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, day_of_week, hour_start, hour_end, timezone, max_requests_per_hour
		FROM scan_windows WHERE source_id = $1
		ORDER BY day_of_week, hour_start`, sourceID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var windows []Window
	for rows.Next() {
		var w Window
		if err := rows.Scan(&w.ID, &w.DOW, &w.HourStart, &w.HourEnd, &w.Timezone, &w.MaxPerHour); err != nil {
			return nil, false, err
		}
		windows = append(windows, w)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(windows) == 0 {
		return nil, false, nil // окон нет — не warming, не настроен
	}

	total, err := TotalScans(ctx, pool, sourceID)
	if err != nil {
		return nil, false, err
	}
	warmingUp := total < s.MinObsForTuning

	var yields map[string]SlotAgg
	if !warmingUp {
		yields, err = SlotYields(ctx, pool, sourceID, s.MAWindowDays)
		if err != nil {
			return nil, false, err
		}
	}

	weights := windowWeights(windows, yields, warmingUp, s.ExplorationFraction)
	for i := range windows {
		windows[i].Weight = weights[i]
		if avg, n := windows[i].poolAvg(yields); n > 0 {
			a := avg
			windows[i].SlotAvg = &a
			windows[i].SlotScans = n
		}
		// Аудит: текущий вычисленный вес (ТЗ §10.2).
		if _, err := pool.Exec(ctx,
			`UPDATE scan_windows SET weight = $2 WHERE id = $1`,
			windows[i].ID, weights[i],
		); err != nil {
			return nil, false, fmt.Errorf("schedule: запись веса окна %d: %w", windows[i].ID, err)
		}
	}
	return windows, warmingUp, nil
}

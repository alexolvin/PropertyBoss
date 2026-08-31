package schedule

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Option — скан, который расписание готово выполнить прямо сейчас.
type Option struct {
	SourceID  string
	ConfigID  int64
	Country   string
	SlotKey   string
	Weight    float64
	Remaining int // остаток бюджета в текущем часовом слоте
	WarmingUp bool
}

// nextSearchConfig — конфигурация поиска источника для следующего
// скана: round-robin — та, чей последний скан старее (никогда не
// сканировалась — первой), при равенстве — меньший id. Все активные
// конфигурации источника по очереди получают бюджет.
func nextSearchConfig(ctx context.Context, pool *pgxpool.Pool, sourceID string) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx, `
		SELECT sc.id
		FROM search_configs sc
		LEFT JOIN (
			SELECT search_config_id, max(started_at) AS last
			FROM scan_runs GROUP BY search_config_id
		) lr ON lr.search_config_id = sc.id
		WHERE sc.source_id = $1 AND sc.active
		ORDER BY lr.last ASC NULLS FIRST, sc.id
		LIMIT 1`, sourceID).Scan(&id)
	if err != nil {
		if err.Error() == "pgx: no rows in result set" {
			return 0, nil
		}
		return 0, err
	}
	return id, nil
}

// Plan — что сканировать сейчас (ТЗ §10): истёкшие кулдауны
// возвращены, для каждого active источника, у которого текущее время
// (в его часовом поясе, ТЗ §10.2) попадает в окно, вычисляется вес и
// остаток бюджета текущего часа. Потолок max_requests_per_hour ×
// rate_factor — жёсткий: остаток <= 0 — источник в плане не участвует
// (ТЗ §10.4). Сортировка: вес по убыванию — бюджет пропорционален
// скользящему среднему выхода (ТЗ §10.3).
func Plan(ctx context.Context, pool *pgxpool.Pool, s *Settings, now time.Time) ([]Option, error) {
	if _, err := RecoverCooldowns(ctx, pool, now); err != nil {
		return nil, err
	}
	sources, err := LoadSourceStates(ctx, pool)
	if err != nil {
		return nil, err
	}
	var opts []Option
	for _, ss := range sources {
		if ss.State != "active" {
			continue
		}
		loc, err := s.TZ(ss.Country)
		if err != nil {
			return nil, err
		}
		t := now.In(loc)
		windows, warmingUp, err := ComputeWeights(ctx, pool, s, ss.ID)
		if err != nil {
			return nil, err
		}
		// Окна одного дня недели не перекрываются (проверяется при
		// init-windows); если всё же несколько покрывают текущий час —
		// консервативно: вес макс., потолок мин.
		var (
			matchedWeight float64
			cap           int
			matched       bool
		)
		for _, w := range windows {
			if w.DOW == int(t.Weekday()) && t.Hour() >= w.HourStart && t.Hour() < w.HourEnd {
				if !matched {
					matched = true
					matchedWeight = w.Weight
					cap = EffectiveCap(w.MaxPerHour, ss.RateFactor)
				} else {
					if w.Weight > matchedWeight {
						matchedWeight = w.Weight
					}
					if c := EffectiveCap(w.MaxPerHour, ss.RateFactor); c < cap {
						cap = c
					}
				}
			}
		}
		if !matched {
			continue
		}
		already, err := ScansThisHour(ctx, pool, ss.ID, loc, now)
		if err != nil {
			return nil, err
		}
		remaining := cap - already
		if remaining <= 0 {
			continue
		}
		cfgID, err := nextSearchConfig(ctx, pool, ss.ID)
		if err != nil {
			return nil, err
		}
		if cfgID == 0 {
			continue // активных конфигураций нет
		}
		opts = append(opts, Option{
			SourceID:  ss.ID,
			ConfigID:  cfgID,
			Country:   ss.Country,
			SlotKey:   SlotKey(int(t.Weekday()), t.Hour()),
			Weight:    matchedWeight,
			Remaining: remaining,
			WarmingUp: warmingUp,
		})
	}
	sort.Slice(opts, func(i, j int) bool {
		if opts[i].Weight != opts[j].Weight {
			return opts[i].Weight > opts[j].Weight
		}
		return opts[i].SourceID < opts[j].SourceID
	})
	return opts, nil
}

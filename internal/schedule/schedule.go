// Package schedule — адаптивное расписание сканирования (этап 11, ТЗ §10).
//
// Модель (ТЗ §10.2–10.3): у каждого источника окна scan_windows
// (день недели × часы в часовом поясе страны объявления) с жёстким
// потолком max_requests_per_hour. Бюджет сканов распределяется
// пропорционально скользящему среднему выхода (новых объектов на скан)
// по слотам (источник, час недели), при этом доля ε всегда
// резервируется на исследование — слот с нулевым выходом не
// отключается насовсем (ТЗ §10.3).
//
// Безопасность (ТЗ §10.4) имеет приоритет над выходом: капча/429 →
// источник немедленно в cooldown с экспоненциальным откатом,
// rate_factor (множитель потолка) падает вдвое и восстанавливается
// только ПОСТЕПЕННО, по шагам, при последующих полных сканах.
//
// Честное ограничение (ТЗ §10.5): пока по источнику накоплено меньше
// min_obs_for_tuning полных сканов, расписание работает на равных
// консервативных весах и помечается warming_up — ранняя «адаптация»
// не выдаётся за статистику.
package schedule

import (
	"fmt"
	"time"

	"propertyboss/internal/config"
)

// Settings — параметры адаптации из конфига (блок schedule:).
// ТЗ §0.1: пороги живут в конфиге, а не числом в коде.
type Settings struct {
	ExplorationFraction float64 // ε, (0, 0.5]
	MAWindowDays        int     // окно скользящего среднего, дней
	MinObsForTuning     int     // warming_up порог, полных сканов на источник
	BackoffBase         time.Duration
	BackoffMultiplier   float64
	BackoffMax          time.Duration
	RecoveryStep        float64           // > 1
	MinRateFactor       float64           // (0, 1]
	Timezones           map[string]string // страна -> IANA
}

// SettingsFrom — Settings из блока config.Schedule (для вызов из cmd).
func SettingsFrom(c *config.Config) *Settings {
	return &Settings{
		ExplorationFraction: c.Schedule.ExplorationFraction,
		MAWindowDays:        c.Schedule.MAWindowDays,
		MinObsForTuning:     c.Schedule.MinObsForTuning,
		BackoffBase:         time.Duration(c.Schedule.BackoffBaseMinutes) * time.Minute,
		BackoffMultiplier:   c.Schedule.BackoffMultiplier,
		BackoffMax:          time.Duration(c.Schedule.BackoffMaxHours) * time.Hour,
		RecoveryStep:        c.Schedule.RecoveryStep,
		MinRateFactor:       c.Schedule.MinRateFactor,
		Timezones:           c.Schedule.CountryTimezones,
	}
}

// TZ — часовой пояс страны объявления (ТЗ §10.2: «пик размещения в
// Праге привязан к рабочему дню Праги», не сервера). Страна без записи
// в конфиге — UTC (защита, а не норма: валидация конфига требует запись
// для каждой страны dashboard.countries).
func (s *Settings) TZ(country string) (*time.Location, error) {
	tz := s.Timezones[country]
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("schedule: IANA-пояс %q для страны %s: %w", tz, country, err)
	}
	return loc, nil
}

// SlotKey — слот «источник, час недели» в поясе страны (ТЗ §10.3):
// 'dow-hour', например '1-9' = понедельник, 9 часов. dow: 0 = воскресенье.
func SlotKey(dow, hour int) string {
	return fmt.Sprintf("%d-%d", dow, hour)
}

// WindowHours — часы, которые покрывает окно [hour_start, hour_end):
// hour_end исключающий (DDL: 1..24, то есть 24 = до полуночи).
// Слоты окна — SlotKey(dow, h) для каждого h в списке.
func WindowHours(hourStart, hourEnd int) []int {
	hs := make([]int, 0, hourEnd-hourStart)
	for h := hourStart; h < hourEnd; h++ {
		hs = append(hs, h)
	}
	return hs
}

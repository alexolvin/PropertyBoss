// ratelimit — общий ограничитель частоты исходящих запросов к источнику (D20).
//
// C27: 429 вернулись на пути прямой верификации, где ограничителя не было
// вовсе. Все исходящие HTTP-пути к источнику — сбор (bazos.Connector),
// обогащение и контроль (bazos.DetailClient), URL-чеки делистинга
// (scan urlCheck), верификационные программы — проходят через один
// пакетный лимитер с одним интервалом из конфига
// (source.request_interval_ms). Кулдауэн по 429 (ТЗ §10.4, D13) остаётся
// задним ограничителем; лимитер — проактивная первая линия.
//
// Пакет отдельный (не в bazos): bazos импортирует scan (типы Fail), а
// scan нужен в urlCheck делистинга — общий базовый пакет без зависимостей.
//
// Лимитер процессный: два параллельных pb-процесса лимитируют себя
// независимо — кросс-процессная частота не ограничена (остаточный риск,
// зафиксирован в D20).
package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Limiter — минимальный интервал между запросами: каждый вызывающий
// резервирует слот не раньше, чем last+interval; слоты сериализуются
// мьютексом, поэтому и конкурентные вызовы получают между собой не
// меньше интервала.
type Limiter struct {
	intervalNS atomic.Int64
	mu         sync.Mutex
	last       time.Time
}

func NewLimiter(interval time.Duration) *Limiter {
	l := &Limiter{}
	l.intervalNS.Store(interval.Nanoseconds())
	return l
}

// SetInterval — интервал между запросами (<=0 — без ограничения).
func (l *Limiter) SetInterval(d time.Duration) {
	l.intervalNS.Store(d.Nanoseconds())
}

// Wait — ждать до времени своего слота (ctx — отмена).
func (l *Limiter) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	interval := time.Duration(l.intervalNS.Load())
	if interval <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	next := l.last.Add(interval)
	if next.Before(now) {
		next = now
	}
	l.last = next
	wait := time.Until(next)
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

var sourceLimiter = NewLimiter(0) // 0 — пока бинарь не поставил из конфига

// SetRequestInterval — общий интервал между запросами к источнику (D20):
// бинарь ставит один раз из конфига (source.request_interval_ms) до
// запуска субкоманды.
func SetRequestInterval(d time.Duration) { sourceLimiter.SetInterval(d) }

var sourceWaits atomic.Int64

// WaitSource — слот общего лимитера; точка входа для всех исходящих
// путей к источнику (D20).
func WaitSource(ctx context.Context) error {
	sourceWaits.Add(1)
	return sourceLimiter.Wait(ctx)
}

// WaitCount — сколько исходящих запросов прошло через лимитер в этом
// процессе (наблюдаемость D20: doctor показывает, что лимитер реально
// на пути; 0 в read-only-процессе штатно).
func WaitCount() int64 { return sourceWaits.Load() }

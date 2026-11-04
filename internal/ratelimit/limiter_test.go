package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

// D20: слоты лимитера — не раньше, чем last+interval, сериализованы.

func TestWaitZeroInterval(t *testing.T) {
	l := NewLimiter(0)
	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if d := time.Since(start); d > 5*time.Millisecond {
		t.Fatalf("интервал 0, а Wait занял %v", d)
	}
}

// Три последовательных слота с интервалом 50мс = два интервала между
// запросами: суммарно >= ~100мс.
func TestWaitSequentialSpacing(t *testing.T) {
	l := NewLimiter(50 * time.Millisecond)
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatalf("Wait[%d]: %v", i, err)
		}
	}
	if d := time.Since(start); d < 95*time.Millisecond {
		t.Fatalf("3 слота с интервалом 50мс должны занять не меньше ~100мс, заняли %v", d)
	}
}

// Конкурентные вызовы получают слоты с размахом не меньше интервала
// (с запасом 25мс на планировщик).
func TestWaitConcurrentSlots(t *testing.T) {
	const n = 4
	const interval = 60 * time.Millisecond
	l := NewLimiter(interval)
	var mu sync.Mutex
	times := make([]time.Time, 0, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Wait(context.Background()); err != nil {
				t.Errorf("Wait: %v", err)
				return
			}
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(times) != n {
		t.Fatalf("получено %d возвратов, ждали %d", len(times), n)
	}
	// Возвраты идут по порядку слотов (порядок записи под мьютексом).
	for i := 1; i < len(times); i++ {
		if d := times[i].Sub(times[i-1]); d < interval-25*time.Millisecond {
			t.Fatalf("слоты %d и %d: размах %v меньше интервала %v (запас 25мс)", i-1, i, d, interval)
		}
	}
}

func TestWaitCancelledCtx(t *testing.T) {
	l := NewLimiter(50 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("ожидали ошибку от уже отменённого контекста")
	}
}

// До вызова бинаря (в тестах) общий лимитер выключен — паузы нет.
func TestWaitSourceDefaultOff(t *testing.T) {
	start := time.Now()
	if err := WaitSource(context.Background()); err != nil {
		t.Fatalf("WaitSource: %v", err)
	}
	if d := time.Since(start); d > 5*time.Millisecond {
		t.Fatalf("WaitSource не должен паузить, занял %v", d)
	}
}

package notify

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DiskInfo — замер свободного места (ТЗ §3.2).
type DiskInfo struct {
	Path     string
	TotalGiB float64
	FreeGiB  float64
	// FreePct — доля свободного места, 0..100 (Bavail/Blocks).
	FreePct float64
}

// MeasureDisk — statfs пути. «Свободно» — Bavail (доступно
// непривилегированному пользователю), а не Bfree (ядро держит
// резерв для root): оператор освобождает место как непривилегированный.
// Только Linux (цель — vzu5-omi, Termux/Android, ТЗ §3); на других
// платформах syscall.Statfs — compile-time ошибка, и это честно.
func MeasureDisk(path string) (*DiskInfo, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil, fmt.Errorf("notify: statfs %s: %w", path, err)
	}
	bsize := uint64(st.Bsize)
	total := st.Blocks * bsize
	if total == 0 {
		return nil, fmt.Errorf("notify: %s: filesystem без блоков (statfs.blocks=0)", path)
	}
	free := st.Bavail * bsize
	const gib = 1 << 30
	return &DiskInfo{
		Path:     path,
		TotalGiB: float64(total) / gib,
		FreeGiB:  float64(free) / gib,
		FreePct:  float64(free) / float64(total) * 100,
	}, nil
}

// CheckDisk — ТЗ §3.2: оповещение ДО достижения критического порога.
// Алерт ставится в очередь, только если свободного места меньше
// criticalPct И с последнего disk_low (любого статуса) прошло больше
// realertMinutes: состояние диска не меняется заметно за минуты,
// повторный алерт каждые 15 минут — шум. Окно считается от
// created_at (миграция 0021), а не sent_at: если доставка сломана
// (нет токена), очередь не заполняется дублями.
// Возврат: (замер, алерт поставлен в очередь, ошибка).
func CheckDisk(ctx context.Context, pool *pgxpool.Pool, path string, criticalPct float64, realertMinutes int) (*DiskInfo, bool, error) {
	info, err := MeasureDisk(path)
	if err != nil {
		return nil, false, err
	}
	if info.FreePct >= criticalPct {
		return info, false, nil // порог не достигнут — тихо
	}
	var last *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT max(created_at) FROM notifications WHERE kind = 'disk_low'`).Scan(&last); err != nil {
		return info, false, fmt.Errorf("notify: последнее disk_low: %w", err)
	}
	if last != nil && time.Since(*last) < time.Duration(realertMinutes)*time.Minute {
		return info, false, nil // внутри окна повтора
	}
	if _, err := Enqueue(ctx, pool, "disk_low", map[string]any{
		"path":         info.Path,
		"free_pct":     info.FreePct,
		"free_gib":     info.FreeGiB,
		"total_gib":    info.TotalGiB,
		"critical_pct": criticalPct,
	}); err != nil {
		return info, false, err
	}
	return info, true, nil
}

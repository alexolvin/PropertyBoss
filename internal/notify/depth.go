package notify

// Волна 09.08, решение 2: delivered=false дольше 5 минут — тревога
// вторым каналом.
//
// Предыдущее поведение (C19.2) ставило алерт queue_depth в ту же
// очередь, на которой и возникла проблема: если доставка сломана,
// сама тревога недоставляема — оператор об этом не узнаёт. Теперь
// тревога доставляется в два приёма:
//
//  1. Прямо, в обход очереди (Client.Send) — второй путь доставки:
//     канал жив, а флош сломан — тревога доходит.
//  2. Журнал: прямой невозможен (Telegram не настроен / ошибка) —
//     вызывающий печатает КРИТИЧНУЮ строку; out-of-band канал.
//
// Очередная строка ставится всегда (окно повтора — max(created_at) по
// kind=queue_depth + история в pb notify status); при успехе прямого
// отправления она сразу помечается 'sent' — она действительно
// доставлена, и флош не должен доставлять её повторно.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrDeliveryNotConfigured — прямой отправитель отсутствует
// (Telegram не настроен): тревога — только в журнал.
var ErrDeliveryNotConfigured = errors.New("notify: доставка не настроена — нет Telegram-клиента")

// Undelivered — результат проверки «delivered=false дольше limit».
type Undelivered struct {
	PendingTotal      int
	OlderTotal        int
	OldestAgeMinutes  int
	DirectlyDelivered bool  // тревога дошла оператору прямо, в обход очереди
	EnqueuedID        int64 // строка queue_depth в очереди (окно повтора + история)
	DirectErr         error // nil при DirectlyDelivered; иначе причина срыва
}

// CheckUndelivered — тревога, если в очереди pending-строки старше
// limitMin минут И окно повтора realertMin минут истекло. Возвращает
// nil, если тревожить нечего (очередь пуста, старых строк нет, окно).
// cl может быть nil (Telegram не настроен) — ставится только строка,
// DirectErr=ErrDeliveryNotConfigured; КРИТИЧЕСКАЯ строка в журнал —
// дело вызывающего (у него контекст: schedule / notify send).
func CheckUndelivered(ctx context.Context, pool DB, ns string, cl *Client, limitMin, realertMin int) (*Undelivered, error) {
	var (
		older  int
		pend   int
		oldest *time.Time
	)
	err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE created_at < now() - make_interval(mins => $2)),
		       count(*),
		       min(created_at)
		FROM notifications WHERE queue_ns = $1 AND status = 'pending'`, ns, limitMin).
		Scan(&older, &pend, &oldest)
	if err != nil {
		return nil, fmt.Errorf("notify: глубина очереди: %w", err)
	}
	if pend == 0 || older == 0 {
		return nil, nil
	}

	// Окно повтора: глубина не повторяется чаще раза в realertMin
	// (состояние «флош сломан» не меняется за минуты — как disk_low).
	var last *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT max(created_at) FROM notifications
		WHERE kind = 'queue_depth' AND queue_ns = $1`, ns).Scan(&last); err != nil {
		return nil, fmt.Errorf("notify: последнее queue_depth: %w", err)
	}
	if last != nil && time.Since(*last) < time.Duration(realertMin)*time.Minute {
		return nil, nil
	}

	age := 0
	if oldest != nil {
		age = int(time.Since(*oldest).Minutes())
	}
	payload := map[string]any{
		"pending_total":       pend,
		"pending_older_5min":  older,
		"oldest_age_minutes":  age,
		"depth_limit_minutes": limitMin,
	}
	id, err := Enqueue(ctx, pool, ns, "queue_depth", payload)
	if err != nil {
		return nil, fmt.Errorf("notify: алерт queue_depth в очередь: %w", err)
	}
	ua := &Undelivered{PendingTotal: pend, OlderTotal: older, OldestAgeMinutes: age, EnqueuedID: id}
	if cl == nil {
		ua.DirectErr = ErrDeliveryNotConfigured
		return ua, nil
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("notify: queue_depth payload: %w", err)
	}
	text, err := Render("queue_depth", raw)
	if err != nil {
		return nil, fmt.Errorf("notify: рендер queue_depth: %w", err)
	}
	if err := cl.Send(ctx, text); err != nil {
		// Прямой срыв — строка остаётся pending: после восстановления
		// флош доставит её (оператор получит тревогу с опозданием).
		ua.DirectErr = err
		return ua, nil
	}
	// Доставлено прямо — помечаем строку, чтобы флош не дублировал.
	if _, err := pool.Exec(ctx,
		`UPDATE notifications SET status = 'sent', sent_at = now() WHERE id = $1`, id); err != nil {
		return nil, fmt.Errorf("notify: пометка queue_depth=%d sent после прямого отправления: %w", id, err)
	}
	ua.DirectlyDelivered = true
	return ua, nil
}

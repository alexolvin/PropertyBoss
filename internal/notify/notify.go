// Package notify — доставка уведомлений (этап 8, ТЗ §2).
//
// Telegram — единственный канал, уведомления только (диалога нет).
// Очередь — таблица notifications (миграция 0015): триггеры пишут
// строки со статусом 'pending' (аномалия delisted — этап 6, публикация
// модели ликвидности — этап 7, свободное место на диске — этап 8),
// `pb notify send` (cron-точка, ТЗ §3.4) забирает их по порядку и
// доставляет через Telegram Bot API.
//
// Контракт содержимого (критерий этапа 8, комментарий миграции 0015):
// payload обязан содержать интервал и размер выборки, а не голое
// число — это гарантия Render: каждое поле модели в сообщении
// сопровождается выборкой, по которой оно посчитано.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB — исполнитель операций с очередью: *pgxpool.Pool или pgx.Tx.
type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

// Enqueue — уведомление в очередь (канал telegram, получатель operator).
// kind — машинный тип события (delist_anomaly, disk_low,
// liquidity_model, object_snapshot, test); payload — JSON, обязан
// содержать интервал и размер выборки (критерий этапа 8).
func Enqueue(ctx context.Context, q DB, kind string, payload any) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("notify: payload %q: %w", kind, err)
	}
	var id int64
	err = q.QueryRow(ctx, `
		INSERT INTO notifications (channel, recipient, kind, payload)
		VALUES ('telegram', 'operator', $1, $2) RETURNING id`, kind, raw).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("notify: постановка %q в очередь: %w", kind, err)
	}
	return id, nil
}

// PendingCount — число недоставленных уведомлений.
func PendingCount(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM notifications WHERE status = 'pending'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("notify: очередь: %w", err)
	}
	return n, nil
}

// ErrPermanent — сбой доставки, который повтором не лечится
// (неверный токен, чат не найден — прочие 4xx Telegram). Flush
// останавливает проход по очереди: дальше будет только так же.
type ErrPermanent struct{ Msg string }

func (e *ErrPermanent) Error() string { return e.Msg }

// Flush — доставляет pending-уведомления, не больше limit штук за
// прогон. Строка — одна транзакция: забрать (FOR UPDATE SKIP LOCKED)
// → отрендерить → отправить → пометить sent/failed. Процесс, убитый
// между отправкой и коммитом, оставляет строку pending — следующий
// прогон пришлёт дубль; для алертов оператора дубль допустим,
// потеря — нет (ТЗ §0.4: недоставленное — не доставлено).
// ErrPermanent — выход из прогона с ошибкой (конфиг починят руками).
func Flush(ctx context.Context, pool *pgxpool.Pool, tg *Client, limit int) (sent, failed int, err error) {
	if limit < 1 {
		limit = 1
	}
	for i := 0; i < limit; i++ {
		res, e := flushOne(ctx, pool, tg)
		switch res {
		case rowEmpty:
			return sent, failed, nil // очередь пуста
		case rowSent:
			sent++
		case rowFailed:
			failed++
			log.Printf("notify: сообщение не доставлено: %v", e)
		}
		if e != nil {
			var perm *ErrPermanent
			if errors.As(e, &perm) {
				return sent, failed, fmt.Errorf("notify: постоянный сбой (исправить конфиг, не ретраить): %v", e)
			}
		}
	}
	return sent, failed, nil
}

type rowResult int

const (
	rowEmpty rowResult = iota
	rowSent
	rowFailed
)

// maxErrorLen — потолок текста ошибки в notifications.error: не
// сваливать сюда целиком HTML-ответ стороннего сервиса.
const maxErrorLen = 1000

// flushOne — одна строка очереди в одной транзакции. Блокировка
// строки держится до коммита — второй flusher (SKIP LOCKED) не
// возьмёт её, а откат (смерть процесса) вернёт строку в pending.
func flushOne(ctx context.Context, pool *pgxpool.Pool, tg *Client) (rowResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return rowFailed, fmt.Errorf("notify: транзакция: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // после коммита — no-op

	var (
		id      int64
		kind    string
		payload []byte
	)
	err = tx.QueryRow(ctx, `
		SELECT id, kind, payload FROM notifications
		WHERE status = 'pending' ORDER BY id LIMIT 1
		FOR UPDATE SKIP LOCKED`).Scan(&id, &kind, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Commit(ctx)
		return rowEmpty, nil
	}
	if err != nil {
		return rowFailed, fmt.Errorf("notify: забрать строку: %w", err)
	}

	var sErr error
	if text, rErr := Render(kind, payload); rErr != nil {
		sErr = rErr
	} else if sErr = tg.Send(ctx, text); sErr != nil {
		sErr = fmt.Errorf("id=%d %s: %w", id, kind, sErr)
	}

	if sErr == nil {
		_, err = tx.Exec(ctx, `
			UPDATE notifications SET status = 'sent', sent_at = now()
			WHERE id = $1`, id)
	} else {
		msg := truncate(sErr.Error(), maxErrorLen)
		_, err = tx.Exec(ctx, `
			UPDATE notifications SET status = 'failed', error = $2, sent_at = now()
			WHERE id = $1`, id, msg)
	}
	if err != nil {
		return rowFailed, fmt.Errorf("notify: пометить строку %d: %w", id, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return rowFailed, fmt.Errorf("notify: коммит строки %d: %w", id, err)
	}
	if sErr != nil {
		return rowFailed, sErr
	}
	return rowSent, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// StatusRow — строка для `pb notify status`.
type StatusRow struct {
	ID        int64
	Kind      string
	Status    string
	CreatedAt time.Time
	SentAt    *time.Time
	Error     *string
}

// Status — состояние очереди (сводка + последние строки).
type Status struct {
	Pending int
	Sent    int
	Failed  int
	Recent  []StatusRow
}

// QueueStatus — сводка по статусам и последние limit строк.
func QueueStatus(ctx context.Context, pool *pgxpool.Pool, limit int) (*Status, error) {
	var st Status
	err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'sent'),
			count(*) FILTER (WHERE status = 'failed')
		FROM notifications`).Scan(&st.Pending, &st.Sent, &st.Failed)
	if err != nil {
		return nil, fmt.Errorf("notify: сводка очереди: %w", err)
	}
	if limit < 1 {
		limit = 10
	}
	rows, err := pool.Query(ctx, `
		SELECT id, kind, status, created_at, sent_at, error
		FROM notifications ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("notify: последние строки: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r StatusRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Status, &r.CreatedAt, &r.SentAt, &r.Error); err != nil {
			return nil, fmt.Errorf("notify: чтение строки: %w", err)
		}
		st.Recent = append(st.Recent, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notify: чтение строк: %w", err)
	}
	return &st, nil
}

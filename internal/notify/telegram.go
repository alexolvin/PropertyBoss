package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// maxSendAttempts — попыток на одно сообщение: 429/5xx/сеть ретраятся,
// прочие 4xx — сразу ErrPermanent (повтор не поможет).
const maxSendAttempts = 3

// retryWait — паузы между повторами на 5xx/сетевом сбое, с.
var retryWait = []time.Duration{time.Second, 2 * time.Second}

// Client — Telegram Bot API, только sendMessage (ТЗ §2: бот —
// только уведомления). BaseURL подменяется в тестах (mock-сервер);
// токен/чат — из конфига, в коде и в логах не дублируются.
type Client struct {
	BaseURL string // default https://api.telegram.org (конфиг)
	Token   string
	ChatID  string
	HTTP    *http.Client
	// Sleep — пауза при 429 (retry_after). По умолчанию time.Sleep;
	// в тестах — no-op.
	Sleep func(time.Duration)
}

func (c *Client) defaultClient() {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Sleep == nil {
		c.Sleep = time.Sleep
	}
}

// RateLimitWait — 429: пауза retry_after, потом повтор.
type RateLimitWait struct{ D time.Duration }

func (e *RateLimitWait) Error() string {
	return fmt.Sprintf("notify: Telegram 429, пауза %v", e.D)
}

// tgResponse — поле ok/error_code/description из ответа Bot API.
type tgResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

// Send — отправить текст в chat_id. 429 → пауза retry_after (потолок
// 60 с) и повтор; 5xx/сеть → повтор с нарастающей паузой; прочие 4xx
// → ErrPermanent (неверный токен, чат не найден — ретраем не лечится).
func (c *Client) Send(ctx context.Context, text string) error {
	c.defaultClient()
	if c.Token == "" || c.ChatID == "" {
		return fmt.Errorf("notify: telegram не настроен (токен/чат_id не заданы в конфиге)")
	}
	body, _ := json.Marshal(map[string]string{"chat_id": c.ChatID, "text": text})

	var lastErr error
	for attempt := 0; attempt < maxSendAttempts; attempt++ {
		if attempt > 0 {
			d := retryWait[min(attempt-1, len(retryWait)-1)]
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
			}
		}
		lastErr = c.sendOnce(ctx, body)
		if lastErr == nil {
			return nil
		}
		var perm *ErrPermanent
		if errors.As(lastErr, &perm) {
			return lastErr
		}
		var wait *RateLimitWait
		if errors.As(lastErr, &wait) {
			c.Sleep(wait.D)
		}
	}
	return lastErr
}

// sendOnce — один запрос. Возвращает nil, *ErrPermanent (4xx кроме
// 429) или *RateLimitWait (429, с паузой retry_after).
func (c *Client) sendOnce(ctx context.Context, body []byte) error {
	url := fmt.Sprintf("%s/bot/%s/sendMessage", c.BaseURL, c.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: запрос: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("notify: сеть: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("notify: чтение ответа: %w", err)
	}

	var tg tgResponse
	if err := json.Unmarshal(data, &tg); err != nil {
		// Не-JSON от API — лечится повтором (сбой на прокси).
		return fmt.Errorf("notify: ответ Bot API не JSON (HTTP %d): %s", resp.StatusCode, truncate(string(data), 200))
	}
	switch {
	case resp.StatusCode == http.StatusOK && tg.OK:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		d := 3 * time.Second
		if pa, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
			d = pa
		}
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		return &RateLimitWait{D: d}
	case resp.StatusCode >= 500:
		return fmt.Errorf("notify: Bot API HTTP %d: %s", resp.StatusCode, tg.Description)
	case resp.StatusCode >= 400:
		return &ErrPermanent{Msg: fmt.Sprintf("Bot API HTTP %d: %s", resp.StatusCode, tg.Description)}
	default:
		return &ErrPermanent{Msg: fmt.Sprintf("Bot API ok=false (code %d): %s", tg.ErrorCode, tg.Description)}
	}
}

// retryAfter — заголовок Retry-After: секунды (Telegram шлёт число).
func retryAfter(h string) (time.Duration, bool) {
	s, err := strconv.Atoi(h)
	if err != nil || s < 0 {
		return 0, false
	}
	return time.Duration(s) * time.Second, true
}

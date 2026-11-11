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
	"strings"
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

// chatIDAndThread — разбирает telegram.chat_id: значение может нести
// топик форума как хвост «/topic_id» (напр. «-1001234567890/26»:
// оператор указывает топик группы через «/»). Bot API не принимает
// топик внутри chat_id — это отдельный параметр message_thread_id;
// базовая часть уходит числом, если числовая, иначе строкой @username.
// Грамматика совпадает с валидацией config (telegram.chat_id).
func chatIDAndThread(chatID string) (any, int, error) {
	base, topic := chatID, 0
	if i := strings.IndexByte(chatID, '/'); i >= 0 {
		base = chatID[:i]
		n, err := strconv.Atoi(chatID[i+1:])
		if err != nil || n <= 0 {
			return nil, 0, fmt.Errorf("notify: telegram.chat_id %q: часть после «/» должна быть положительным номером топика", chatID)
		}
		topic = n
	}
	if n, err := strconv.ParseInt(base, 10, 64); err == nil {
		return n, topic, nil
	}
	if strings.HasPrefix(base, "@") {
		return base, topic, nil
	}
	return nil, 0, fmt.Errorf("notify: telegram.chat_id %q: ожидается числовой id или @username (опционально с топиком «/N»)", chatID)
}

// Send — отправить текст в chat_id (опционально в топик «/N»).
// 429 → пауза retry_after (потолок 60 с) и повтор; 5xx/сеть → повтор с
// нарастающей паузой; прочие 4xx → ErrPermanent (неверный токен, чат не
// найден — ретраем не лечится).
func (c *Client) Send(ctx context.Context, text string) error {
	_, err := c.SendResponse(ctx, text)
	return err
}

// SendResponse — отправить текст и вернуть дословный ответ Bot API.
func (c *Client) SendResponse(ctx context.Context, text string) ([]byte, error) {
	c.defaultClient()
	if c.Token == "" || c.ChatID == "" {
		return nil, fmt.Errorf("notify: telegram не настроен (токен/чат_id не заданы в конфиге)")
	}
	chatID, thread, err := chatIDAndThread(c.ChatID)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"chat_id": chatID, "text": text}
	if thread > 0 {
		body["message_thread_id"] = thread
	}
	bodyJSON, _ := json.Marshal(body)

	var lastErr error
	for attempt := 0; attempt < maxSendAttempts; attempt++ {
		if attempt > 0 {
			d := retryWait[min(attempt-1, len(retryWait)-1)]
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
		}
		raw, err := c.sendOnce(ctx, bodyJSON)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		var perm *ErrPermanent
		if errors.As(lastErr, &perm) {
			return raw, lastErr
		}
		var wait *RateLimitWait
		if errors.As(lastErr, &wait) {
			c.Sleep(wait.D)
		}
	}
	return nil, lastErr
}

// sendOnce — один запрос. Возвращает ответ Bot API, nil, *ErrPermanent (4xx кроме
// 429) или *RateLimitWait (429, с паузой retry_after).
func (c *Client) sendOnce(ctx context.Context, body []byte) ([]byte, error) {
	// Каноническая форма Bot API: bot<token>/<method> — «bot» и токен
	// слитно. Со слэшем (/bot/<token>/) запрос попадает на WAF-локацию
	// edge, которая с некоторых IP отвечает 404 «Not Found» в
	// зависимости от клиента (проверено 09.09: тот же запрос в одну
	// минуту — со слэшем 404, без слэша 200).
	url := fmt.Sprintf("%s/bot%s/sendMessage", c.BaseURL, c.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("notify: запрос: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("notify: сеть: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("notify: чтение ответа: %w", err)
	}

	var tg tgResponse
	if err := json.Unmarshal(data, &tg); err != nil {
		// Не-JSON от API — лечится повтором (сбой на прокси).
		return data, fmt.Errorf("notify: ответ Bot API не JSON (HTTP %d): %s", resp.StatusCode, truncate(string(data), 200))
	}
	switch {
	case resp.StatusCode == http.StatusOK && tg.OK:
		return data, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		d := 3 * time.Second
		if pa, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
			d = pa
		}
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		return data, &RateLimitWait{D: d}
	case resp.StatusCode >= 500:
		return data, fmt.Errorf("notify: Bot API HTTP %d: %s", resp.StatusCode, tg.Description)
	case resp.StatusCode >= 400:
		return data, &ErrPermanent{Msg: fmt.Sprintf("Bot API HTTP %d: %s", resp.StatusCode, tg.Description)}
	default:
		return data, &ErrPermanent{Msg: fmt.Sprintf("Bot API ok=false (code %d): %s", tg.ErrorCode, tg.Description)}
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

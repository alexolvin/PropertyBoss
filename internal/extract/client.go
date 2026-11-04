// client.go — OpenAI-совместимый LLM-клиент извлекателя атрибутов
// (ТЗ v4 §4.3). Протокол тот же, что у переводчика
// (internal/translate/llm.go): POST {base_url}/chat/completions;
// поведение при 429/5xx/4xx — то же (пауза retry_after, ретраи,
// ErrPermanent на 4xx — прогон дальше по той же выборке бессмыслен).
package extract

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

// maxAttempts — попыток на одно обращение: 429/5xx/сеть ретраятся,
// прочие 4xx — сразу ErrPermanent.
const maxAttempts = 3

// retryWaits — паузы между повторами на 5xx/сетевом сбое, с.
var retryWaits = []time.Duration{time.Second, 2 * time.Second}

// ErrPermanent — ошибку ретраем не исправить (неверный API-ключ,
// модель не найдена — прочие 4xx): прогон останавливается.
type ErrPermanent struct{ Msg string }

func (e *ErrPermanent) Error() string { return "extract: " + e.Msg }

// Client — OpenAI-совместимый клиент. HTTP подменяется в тестах.
type Client struct {
	BaseURL   string
	APIKey    string
	Model     string
	HTTP      *http.Client
	Sleep     func(time.Duration) // пауза при 429; в тестах — no-op
	MaxTokens int                 // 0 — поле не отправляется (тесты)
}

func (c *Client) defaultClient() {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if c.Sleep == nil {
		c.Sleep = time.Sleep
	}
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model       string    `json:"model"`
	Messages    []chatMsg `json:"messages"`
	Temperature float64   `json:"temperature"`
	// Qwen3 (vLLM): отключаем thinking-режим в chat template. Иначе модель
	// начинает ответ с рассуждения («We need to respond…»), и JSON не
	// парсится с первого символа. На серверах без этого параметра поле
	// игнорируется.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs"`
	MaxTokens          int            `json:"max_tokens,omitempty"`
}

type chatResp struct {
	Model   string `json:"model"`
	Choices []struct {
		Message chatMsg `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete — один диалог (системный + пользовательский промпт) → текст
// ответа, имя модели, реально обслуживавшей запрос, и стоимость в
// токенах. temperature 0: извлечение — детерминированная задача.
func (c *Client) Complete(ctx context.Context, system, user string) (text, model string, tokens int, err error) {
	c.defaultClient()
	if c.APIKey == "" {
		return "", "", 0, fmt.Errorf("extract: API-ключ не задан (extract.api_key в конфиге)")
	}
	body, _ := json.Marshal(chatReq{
		Model:              c.Model,
		Messages:           []chatMsg{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Temperature:        0,
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
		MaxTokens:          c.MaxTokens,
	})

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			d := retryWaits[min(attempt-1, len(retryWaits)-1)]
			select {
			case <-ctx.Done():
				return "", "", 0, ctx.Err()
			case <-time.After(d):
			}
		}
		text, model, tokens, err = c.completeOnce(ctx, body)
		if err == nil {
			return text, model, tokens, nil
		}
		var perm *ErrPermanent
		if errors.As(err, &perm) {
			return "", "", 0, err
		}
		lastErr = err
	}
	return "", "", 0, lastErr
}

func (c *Client) completeOnce(ctx context.Context, body []byte) (string, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", "", 0, fmt.Errorf("extract: запрос: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("extract: сеть: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", 0, fmt.Errorf("extract: чтение ответа: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		d := 3 * time.Second
		if pa, ok := retryAfterSeconds(resp.Header.Get("Retry-After")); ok {
			d = pa
		}
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		c.Sleep(d)
		return "", "", 0, fmt.Errorf("extract: API 429, пауза %v", d)
	}

	var cr chatResp
	if err := json.Unmarshal(data, &cr); err != nil {
		return "", "", 0, fmt.Errorf("extract: ответ API не JSON (HTTP %d)", resp.StatusCode)
	}
	switch {
	case resp.StatusCode == http.StatusOK && len(cr.Choices) > 0:
		text := cr.Choices[0].Message.Content
		if text == "" {
			return "", "", 0, fmt.Errorf("extract: API вернул пустой ответ")
		}
		tokens := cr.Usage.TotalTokens
		if tokens == 0 {
			tokens = cr.Usage.PromptTokens + cr.Usage.CompletionTokens
		}
		model := cr.Model
		if model == "" {
			model = c.Model
		}
		return text, model, tokens, nil
	case resp.StatusCode >= 500:
		msg := "API"
		if cr.Error != nil {
			msg = cr.Error.Message
		}
		return "", "", 0, fmt.Errorf("extract: API HTTP %d: %s", resp.StatusCode, msg)
	default:
		msg := "API"
		if cr.Error != nil {
			msg = cr.Error.Message
		}
		return "", "", 0, &ErrPermanent{Msg: fmt.Sprintf("API HTTP %d: %s", resp.StatusCode, msg)}
	}
}

func retryAfterSeconds(h string) (time.Duration, bool) {
	s, err := strconv.Atoi(h)
	if err != nil || s < 0 {
		return 0, false
	}
	return time.Duration(s) * time.Second, true
}

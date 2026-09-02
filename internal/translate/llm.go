// LLM-клиент переводчика описаний (этап 10, ТЗ §11).
//
// OpenAI-совместимый API (POST {base_url}/chat/completions): перевод
// description_original в целевой язык (ru/en). Из ответа сохраняем имя
// модели, реально обслуживавшей запрос, и стоимость в токенах (ТЗ §11:
// «каждый перевод хранит model, translated_at, token_cost»).
package translate

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

// maxTranslateAttempts — попыток на одно обращение: 429/5xx/сеть
// ретраятся, прочие 4xx — сразу ErrPermanent (ретраем не лечится).
const maxTranslateAttempts = 3

// llmRetryWait — паузы между повторами на 5xx/сетевом сбое, с.
// Тестовый хук: в тестах подменяется нулями.
var llmRetryWait = []time.Duration{time.Second, 2 * time.Second}

// ErrPermanent — ошибка, которую ретраем не исправить (неверный API-ключ,
// чат/модель не найдены — прочие 4xx). Прогон `pb translate run`
// останавливается: дальше по той же выборке ситуация та же, оператору это
// видно одной ошибкой.
type ErrPermanent struct{ Msg string }

func (e *ErrPermanent) Error() string { return "translate: " + e.Msg }

// ErrRateLimit — API вернул 429 и исчерпаны попытки: сервер просит ждать.
// Прогон останавливается (дальше — тот же лимит) и продолжится следующим
// cron. Не ошибка конфигурации: объект просто не переведён в этот раз.
type ErrRateLimit struct {
	Msg string
	d   time.Duration
}

func (e *ErrRateLimit) Error() string { return "translate: " + e.Msg }

// langNames — человекочитаемое имя языка для промпта (не только код).
var langNames = map[string]string{
	"ru": "Russian",
	"en": "English",
}

// Client — OpenAI-совместимый LLM-клиент. BaseURL/Model — из конфига,
// APIKey не дублируется в логах. HTTP подменяется в тестах (mock-сервер).
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
	// Sleep — пауза при 429 (retry_after). По умолчанию time.Sleep;
	// в тестах — no-op.
	Sleep func(time.Duration)
}

func (c *Client) defaultClient() {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if c.Sleep == nil {
		c.Sleep = time.Sleep
	}
}

// chatReq/chatResp — минимум OpenAI chat/completions, который нужен.
type chatReq struct {
	Model    string    `json:"model"`
	Messages []chatMsg `json:"messages"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Model   string        `json:"model"`
	Choices []chatChoice  `json:"choices"`
	Usage   chatUsage     `json:"usage"`
	Error   *chatAPIError `json:"error"`
}

type chatChoice struct {
	Message chatMsg `json:"message"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Result — исход перевода: текст, имя модели, реально обслуживавшей
// запрос, и стоимость в токенах (prompt + completion).
type Result struct {
	Text   string
	Model  string
	Tokens int
}

// Translate — перевести text в язык lang (ISO-639-1: ru/en). Возвращает
// перевод, имя модели и стоимость в токенах. Ошибки: *ErrPermanent
// (прогон останавливается), *ErrRateLimit (прогон останавливается до
// следующего cron) или обычный error (один объект не переведён —
// следующий cron повторит).
func (c *Client) Translate(ctx context.Context, text, lang string) (Result, error) {
	c.defaultClient()
	if c.APIKey == "" {
		return Result{}, fmt.Errorf("translate: API-ключ не задан (translate.api_key в конфиге)")
	}
	name, ok := langNames[lang]
	if !ok {
		// Целевые языки фиксированы ТЗ (§11: ru, en); неизвестный язык —
		// ошибка конфигурации прогона, а не «разумное значение».
		return Result{}, fmt.Errorf("translate: целевой язык %q не поддерживается", lang)
	}

	var prompt, user string
	prompt = "You are a professional real-estate listing translator. " +
		"Translate the user's message into " + name + " (" + lang + "). " +
		"Output ONLY the translation — no explanations, no quotes, no preamble."
	user = text
	body, _ := json.Marshal(chatReq{
		Model:    c.Model,
		Messages: []chatMsg{{Role: "system", Content: prompt}, {Role: "user", Content: user}},
	})

	var lastErr error
	for attempt := 0; attempt < maxTranslateAttempts; attempt++ {
		if attempt > 0 {
			d := llmRetryWait[min(attempt-1, len(llmRetryWait)-1)]
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(d):
			}
		}
		res, err := c.translateOnce(ctx, body)
		if err == nil {
			return res, nil
		}
		var perm *ErrPermanent
		if errors.As(err, &perm) {
			return Result{}, err
		}
		var rl *ErrRateLimit
		if errors.As(err, &rl) {
			c.Sleep(rl.d)
		}
		lastErr = err
	}
	return Result{}, lastErr
}

// translateOnce — один запрос. Ошибка nil / *ErrPermanent / *ErrRateLimit
// (с паузой) / обычный error (5xx, сеть — ретраится).
func (c *Client) translateOnce(ctx context.Context, body []byte) (Result, error) {
	url := c.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("translate: запрос: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("translate: сеть: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("translate: чтение ответа: %w", err)
	}

	// 429 — отдельный путь: пауза retry_after и повтор (ТЗ §13-дух:
	// сохранить доступ, не долбить API).
	if resp.StatusCode == http.StatusTooManyRequests {
		d := 3 * time.Second
		if pa, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
			d = pa
		}
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		return Result{}, &ErrRateLimit{Msg: fmt.Sprintf("API 429, пауза %v", d), d: d}
	}

	var cr chatResp
	if err := json.Unmarshal(data, &cr); err != nil {
		// Не-JSON от API — лечится повтором (сбой на прокси).
		return Result{}, fmt.Errorf("translate: ответ API не JSON (HTTP %d): %s", resp.StatusCode, truncate(string(data), 200))
	}
	switch {
	case resp.StatusCode == http.StatusOK && len(cr.Choices) > 0:
		tokens := cr.Usage.TotalTokens
		if tokens == 0 {
			tokens = cr.Usage.PromptTokens + cr.Usage.CompletionTokens
		}
		model := cr.Model
		if model == "" {
			model = c.Model
		}
		text := cr.Choices[0].Message.Content
		text = strings.TrimSpace(text)
		if text == "" {
			// Пустой перевод — не перевод (ТЗ §11: псевдоперевод запрещён).
			return Result{}, fmt.Errorf("translate: API вернул пустой перевод")
		}
		return Result{Text: text, Model: model, Tokens: tokens}, nil
	case resp.StatusCode >= 500:
		msg := "API"
		if cr.Error != nil {
			msg = cr.Error.Message
		}
		return Result{}, fmt.Errorf("translate: API HTTP %d: %s", resp.StatusCode, msg)
	case resp.StatusCode >= 400:
		msg := "API"
		if cr.Error != nil {
			msg = cr.Error.Message
		}
		return Result{}, &ErrPermanent{Msg: fmt.Sprintf("API HTTP %d: %s", resp.StatusCode, msg)}
	default:
		msg := "API"
		if cr.Error != nil {
			msg = cr.Error.Message
		}
		return Result{}, &ErrPermanent{Msg: fmt.Sprintf("API (code %d): %s", resp.StatusCode, msg)}
	}
}

// retryAfter — заголовок Retry-After: секунды.
func retryAfter(h string) (time.Duration, bool) {
	s, err := strconv.Atoi(h)
	if err != nil || s < 0 {
		return 0, false
	}
	return time.Duration(s) * time.Second, true
}

// truncate — обрезка для сообщений об ошибке (не тащим тело ответа в лог).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

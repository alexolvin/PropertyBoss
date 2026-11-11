package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LLMState — состояние доступности LLM-сервера.
type LLMState struct {
	UnavailableSince *time.Time `json:"unavailable_since,omitempty"`
	LastCheck        time.Time  `json:"last_check"`
	LastError        string     `json:"last_error,omitempty"`
}

// CheckHealth проверяет GET {baseURL}/models с таймаутом 5 с (ТЗ-R1 R1.5).
// Сохраняет состояние в data/llm_state.json для отслеживания времени начала недоступности.
func CheckHealth(ctx context.Context, baseURL string) (bool, *LLMState, error) {
	if baseURL == "" {
		return false, nil, fmt.Errorf("baseURL не задан")
	}
	modelsURL := strings.TrimRight(baseURL, "/") + "/models"

	statePath := filepath.Join("data", "llm_state.json")
	var st LLMState
	if data, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(data, &st)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return false, &st, err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	now := time.Now().UTC()
	st.LastCheck = now

	if err != nil || resp.StatusCode >= 400 {
		errStr := "недоступен"
		if err != nil {
			errStr = err.Error()
		} else {
			errStr = fmt.Sprintf("HTTP %d", resp.StatusCode)
			resp.Body.Close()
		}
		if st.UnavailableSince == nil {
			st.UnavailableSince = &now
		}
		st.LastError = errStr
		_ = saveJSON(statePath, st)
		return false, &st, fmt.Errorf("%s", errStr)
	}
	resp.Body.Close()

	st.UnavailableSince = nil
	st.LastError = ""
	_ = saveJSON(statePath, st)
	return true, &st, nil
}

func saveJSON(path string, v any) error {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

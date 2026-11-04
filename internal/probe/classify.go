package probe

import (
	"strings"
	"time"
)

// Prev — предыдущая проба кандидата (два прогона для dead/captcha/challenge/blocked).
type Prev struct {
	Class    string
	Reason   string
	ProbedAt time.Time
}

func needsTwoRuns(class string) bool {
	switch class {
	case "dead", "excluded_captcha", "excluded_challenge", "excluded_blocked":
		return true
	}
	return false
}

func isBazos(domain string) bool {
	d := strings.ToLower(strings.TrimPrefix(domain, "www."))
	return d == "bazos.cz" || d == "reality.bazos.cz"
}

// DecideClass — наблюдаемый класс этого прогона → класс для записи.
// Для классов §4.2 с двумя прогонами первый оставляет pending.
func DecideClass(domain, observed, observeReason string, prev *Prev) (class, reason string) {
	if isBazos(domain) {
		return "already_connected", "bazos.cz — действующий коннектор bazos-reality"
	}
	if observed == "" {
		observed = "pending"
	}
	if !needsTwoRuns(observed) {
		if observeReason == "" {
			observeReason = "класс " + observed
		}
		return observed, observeReason
	}
	reason1 := "наблюдение 1/2 класса " + observed + ": " + observeReason
	if prev == nil || prev.ProbedAt.IsZero() {
		return "pending", reason1
	}
	if time.Since(prev.ProbedAt) < time.Hour {
		return "pending", reason1 + " (повтор раньше 1 ч)"
	}
	if prev.Class == observed || strings.Contains(prev.Reason, observed) {
		return observed, "подтверждено вторым прогоном ≥1 ч: " + observeReason
	}
	return "pending", reason1
}

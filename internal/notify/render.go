package notify

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"propertyboss/internal/money"
)

// Render — payload уведомления из очереди → текст Telegram-сообщения.
//
// Язык — русский: получатель — оператор, ТЗ и вся документация
// проекта на русском (допущение, см. отчёт этапа 8).
//
// Критерий этапа 8 (комментарий миграции 0015): payload обязан
// содержать интервал и размер выборки, а не голое число. Поэтому
// каждый модельный выход здесь сопровождается выборо́й, по которой
// он посчитан; отсутствие обязательного поля — ошибка, а не
// «разумное значение по умолчанию».
func Render(kind string, payload []byte) (string, error) {
	m, err := payloadMap(kind, payload)
	if err != nil {
		return "", err
	}
	switch kind {
	case "delist_anomaly":
		return renderDelistAnomaly(m)
	case "disk_low":
		return renderDiskLow(m)
	case "liquidity_model":
		return renderLiquidityModel(m)
	case "object_snapshot":
		return renderObjectSnapshot(m)
	case "test":
		return "PropertyBoss: тестовое сообщение — канал Telegram работает (этап 8).", nil
	default:
		return "", fmt.Errorf("notify: неизвестный kind %q", kind)
	}
}

// requiredPayload — обязательные поля payload для каждого kind.
var requiredPayload = map[string][]string{
	"delist_anomaly": {"source_id", "active_objects", "candidates", "share_pct", "max_share_pct"},
	"disk_low":       {"path", "free_pct", "free_gib", "total_gib", "critical_pct"},
	"liquidity_model": {"country", "deal_type", "model_version", "horizon_days",
		"n_completed_events", "min_events", "n_person_periods",
		"train_cutoff_at", "n_train", "n_test", "previous_status"},
	"object_snapshot": {"object_id", "country", "deal_type", "status"},
}

// payloadMap — JSON → map + проверка обязательных полей. Пропущенное
// обязательное поле — ошибка: «разумное значение по умолчанию» здесь
// превратило бы алерт в враньё.
func payloadMap(kind string, payload []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, fmt.Errorf("notify: payload %q: JSON: %w", kind, err)
	}
	for _, k := range requiredPayload[kind] {
		if _, ok := m[k]; !ok {
			return nil, fmt.Errorf("notify: payload %q: нет обязательного поля %q", kind, k)
		}
	}
	return m, nil
}

func into(m map[string]any, v any) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return err
	}
	return nil
}

func renderDelistAnomaly(m map[string]any) (string, error) {
	p := struct {
		SourceID      string  `json:"source_id"`
		ActiveObjects int     `json:"active_objects"`
		Candidates    int     `json:"candidates"`
		SharePct      float64 `json:"share_pct"`
		MaxSharePct   float64 `json:"max_share_pct"`
	}{}
	if err := into(m, &p); err != nil {
		return "", fmt.Errorf("notify: delist_anomaly: %w", err)
	}
	return fmt.Sprintf(
		"⚠️ Аномальный delist-прогон: источник %s — исчезло %d из %d активных объектов (%.1f%%), порог %.0f%%.\n"+
			"Изменения статусов НЕ применены: вероятна смена вёрстки сайта или сбой сканера (ТЗ §8.2, защита №3). Проверьте источник.",
		p.SourceID, p.Candidates, p.ActiveObjects, p.SharePct, p.MaxSharePct), nil
}

func renderDiskLow(m map[string]any) (string, error) {
	p := struct {
		Path        string  `json:"path"`
		FreePct     float64 `json:"free_pct"`
		FreeGiB     float64 `json:"free_gib"`
		TotalGiB    float64 `json:"total_gib"`
		CriticalPct float64 `json:"critical_pct"`
	}{}
	if err := into(m, &p); err != nil {
		return "", fmt.Errorf("notify: disk_low: %w", err)
	}
	return fmt.Sprintf(
		"💾 Мало свободного места: %s — свободно %.1f%% (%.1f из %.1f GiB), порог %.0f%% (ТЗ §3.2: алерт ДО критической точки).\n"+
			"Освободите место или перенесите данные — иначе резервные копии (WAL/pg_dump) перестанут укладываться.",
		p.Path, p.FreePct, p.FreeGiB, p.TotalGiB, p.CriticalPct), nil
}

func renderLiquidityModel(m map[string]any) (string, error) {
	p := struct {
		Country        string   `json:"country"`
		DealType       string   `json:"deal_type"`
		ModelVersion   string   `json:"model_version"`
		HorizonDays    int      `json:"horizon_days"`
		NCompleted     int      `json:"n_completed_events"`
		MinEvents      int      `json:"min_events"`
		NPersonPeriods int      `json:"n_person_periods"`
		NParams        int      `json:"n_params"`
		TrainCutoffAt  string   `json:"train_cutoff_at"`
		NTrain         int      `json:"n_train"`
		NTest          int      `json:"n_test"`
		CIndex         *float64 `json:"c_index"`
		Brier          *float64 `json:"brier_score"`
		MaxCalibDev    *float64 `json:"max_calib_dev"`
		PreviousStatus string   `json:"previous_status"`
	}{}
	if err := into(m, &p); err != nil {
		return "", fmt.Errorf("notify: liquidity_model: %w", err)
	}
	return fmt.Sprintf(
		"✅ Модель ликвидности опубликована: %s/%s.\n"+
			"Версия %s, горизонт %d дн.\n"+
			"Выборка: %d завершённых событий (порог %d), %d person-period строк (%d параметров).\n"+
			"Разбиение по времени: %d строк обучения (до %s) / %d строк проверки.\n"+
			"Валидация: C-index %s, Brier %s, макс. отклонение калибровки %s.\n"+
			"Переход из статуса: %s.",
		p.Country, p.DealType,
		p.ModelVersion, p.HorizonDays,
		p.NCompleted, p.MinEvents, p.NPersonPeriods, p.NParams,
		p.NTrain, formatCutoff(p.TrainCutoffAt), p.NTest,
		fmtMetric(p.CIndex), fmtMetric(p.Brier), fmtDevPoints(p.MaxCalibDev),
		p.PreviousStatus), nil
}

func renderObjectSnapshot(m map[string]any) (string, error) {
	p := struct {
		ObjectID         int64   `json:"object_id"`
		Country          string  `json:"country"`
		DealType         string  `json:"deal_type"`
		Status           string  `json:"status"`
		PriceMinor       *int64  `json:"price_minor"`
		Currency         *string `json:"currency"`
		CurrencyExponent *int    `json:"currency_exponent"`
		DaysOnMarket     *int    `json:"days_on_market"`
		Valuation        *struct {
			DeviationPct    *float64 `json:"deviation_pct"`
			DeviationReason string   `json:"deviation_reason"`
			IntervalLow     *int64   `json:"interval_low_minor"`
			IntervalHigh    *int64   `json:"interval_high_minor"`
			SampleSize      int      `json:"sample_size"`
			RSquared        *float64 `json:"r_squared"`
			ZoneFallback    bool     `json:"zone_fallback"`
			ModelVersion    string   `json:"model_version"`
		} `json:"valuation"`
		Hazard *struct {
			Probability   *float64 `json:"probability"`
			NullReason    string   `json:"null_reason"`
			HorizonDays   int      `json:"horizon_days"`
			ModelVersion  string   `json:"model_version"`
			EventsInTrain int      `json:"events_in_training"`
		} `json:"hazard"`
	}{}
	if err := into(m, &p); err != nil {
		return "", fmt.Errorf("notify: object_snapshot: %w", err)
	}

	cur, exp := "", 0
	if p.Currency != nil && p.CurrencyExponent != nil {
		cur, exp = *p.Currency, *p.CurrencyExponent
	}
	priceStr := func(minor *int64) string {
		if minor == nil || cur == "" {
			return "н/д"
		}
		return formatPrice(*minor, cur, exp)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📋 Объект %d (%s/%s, %s):", p.ObjectID, p.Country, p.DealType, p.Status)
	if p.PriceMinor != nil && cur != "" {
		fmt.Fprintf(&b, " цена %s,", formatPrice(*p.PriceMinor, cur, exp))
	} else {
		b.WriteString(" цена: нет в объявлении,")
	}
	if p.DaysOnMarket != nil {
		fmt.Fprintf(&b, " на рынке %d дн.", *p.DaysOnMarket)
	}
	b.WriteString("\n")

	// Оценка (ТЗ §7): интервал + размер выборки — критерий этапа 8.
	if p.Valuation == nil {
		b.WriteString("Оценка: нет — оценка не выполнялась (нет данных).\n")
	} else if p.Valuation.DeviationPct == nil {
		fmt.Fprintf(&b, "Оценка: нет — %s.\n", p.Valuation.DeviationReason)
	} else if p.Valuation.IntervalLow == nil || p.Valuation.IntervalHigh == nil {
		// Оценка посчитана, но интервала нет — показываем отклонение
		// с честной пометкой, а не молчим.
		fmt.Fprintf(&b,
			"Оценка: отклонение от модели %+.1f%%; интервал: не построен; выборка n=%d (модель %s).\n",
			*p.Valuation.DeviationPct*100, p.Valuation.SampleSize, p.Valuation.ModelVersion)
	} else {
		fall := ""
		if p.Valuation.ZoneFallback {
			fall = ", зона: fallback (мало наблюдений в зоне)"
		}
		fmt.Fprintf(&b,
			"Оценка: отклонение от модели %+.1f%%; интервал %s — %s; выборка n=%d (r²=%s, модель %s%s).\n",
			*p.Valuation.DeviationPct*100,
			priceStr(p.Valuation.IntervalLow), priceStr(p.Valuation.IntervalHigh),
			p.Valuation.SampleSize, fmtMetric(p.Valuation.RSquared),
			p.Valuation.ModelVersion, fall)
	}

	// Вероятность ухода с рынка (ТЗ §9.3: «уход», не «продажа»).
	if p.Hazard == nil {
		b.WriteString("Вероятность ухода с рынка: нет — модель ликвидности не запускалась.\n")
	} else if p.Hazard.Probability == nil {
		fmt.Fprintf(&b, "Вероятность ухода с рынка: нет — %s.\n", p.Hazard.NullReason)
	} else {
		fmt.Fprintf(&b, "Вероятность ухода с рынка за %d дн.: %s (модель %s, %d событий в обучении).\n",
			p.Hazard.HorizonDays, fmtPct(*p.Hazard.Probability), p.Hazard.ModelVersion, p.Hazard.EventsInTrain)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// --- форматирование ---------------------------------------------------

// formatPrice — деньги (минорные единицы, экспонента из справочника
// currencies) в читаемом виде: 450000000 → «450 000 CZK».
func formatPrice(minor int64, code string, exponent int) string {
	m := money.Money{Minor: minor, Currency: money.Currency{Code: code, Exponent: exponent}}
	return groupNum(strings.TrimSuffix(m.String(), ".00")) + " " + code
}

// groupNum — разряды через пробел: "4500000" → "4 500 000".
// Знак и десятичная часть (r², отклонения сюда не приходят, но
// функция общая) сохраняются.
func groupNum(s string) string {
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, frac := s, ""
	if di := strings.IndexByte(s, '.'); di >= 0 {
		intPart, frac = s[:di], s[di+1:]
	}
	var b []byte
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b = append(b, ' ')
		}
		b = append(b, byte(r))
	}
	out := string(b)
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// fmtMetric — метрика 0..1 или «н/д» (например, C-index без
// сравнимых пар).
func fmtMetric(v *float64) string {
	if v == nil {
		return "н/д"
	}
	return fmt.Sprintf("%.3f", *v)
}

// fmtDevPoints — отклонение калибровки, доля → процентные пункты.
func fmtDevPoints(v *float64) string {
	if v == nil {
		return "н/д"
	}
	return fmt.Sprintf("%.1f п.п.", *v*100)
}

// fmtPct — вероятность 0..1 → «12.3%».
func fmtPct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

// formatCutoff — дата/время разреза ТЗ §9.4 (обучение до Т).
// RFC3339 из payload; если не распарсилось — как есть (честно).
func formatCutoff(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

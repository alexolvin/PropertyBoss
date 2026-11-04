// dataset.go — формирование person-period выборки (ТЗ §9.2).
//
// Наблюдение объекта разбивается на недельные интервалы от начала
// экспозиции до delisted_at (или до текущего момента, если объект
// активен). Активные объекты дают только нули и остаются в выборке —
// это и есть корректный учёт правого цензурирования (ТЗ §9.1–9.2).
package liquidity

import (
	"math"
	"time"
)

// week — недельная дискретизация (ТЗ §9.2; §14.5.2: время ухода
// интервально цензурировано, точность до дня была бы ложной).
const week = 7 * 24 * time.Hour

// PricePoint — точка price_history: момент, когда цена стала
// известной системе.
type PricePoint struct {
	At    time.Time
	Minor int64
}

// ValPoint — строка valuations с price_deviation != NULL: предиктор
// price_deviation (ТЗ §9.2) на начало интервалов.
type ValPoint struct {
	At        time.Time
	Deviation float64
}

// Obj — объект в выборке: полная история экспозиции.
type Obj struct {
	ID     int64
	Status string // "active" | "delisted"
	// Начало экспозиции: min(first_seen_at, мин posted_at по всем
	// наблюдениям) (ТЗ §14.5.1). posted_at площадки ненадёжен
	// (обновляется при редактировании) — минимум по истории
	// восстанавливает изначальную дату как это возможно.
	Start      time.Time
	End        time.Time // delisted_at (delisted) или asOf (active)
	ZoneID     *int64
	Attrs      map[string]string
	Unreliable bool         // posted_date_unreliable: исключается из обучения (ТЗ §14.5.1)
	Prices     []PricePoint // по возрастанию At
	ValDevs    []ValPoint   // по возрастанию At
}

// Period — недельный интервал объекта (строка person-period).
type Period struct {
	Obj    int // индекс в Dataset.Objects
	Week   int // номер интервала от начала экспозиции, 0-based
	Start  time.Time
	End    time.Time
	Target int // 1 — объект исчез в этом интервале, иначе 0

	// Изменяющиеся во времени предикторы, значения на НАЧАЛО
	// интервала (ТЗ §9.2): подстановка финальной цены в ранние
	// интервалы — утечка целевой переменной из будущего, запрещена.
	Reductions int      // число снижений цены с начала экспозиции
	DropPct    float64  // суммарное снижение, % от стартовой цены
	DaysSince  int      // дней с последнего изменения цены
	Increased  int      // 1 — было повышение цены
	ValDev     *float64 // price_deviation (valuations) на начало интервала
}

// Dataset — person-period выборка для одной (страна, тип сделки).
type Dataset struct {
	Objects []Obj
	Periods []Period
}

// NewDataset — разбивает объекты на недельные интервалы и считает
// для каждого интервала временные предикторы.
func NewDataset(objs []Obj) *Dataset {
	d := &Dataset{Objects: objs}
	for i, o := range objs {
		d.Periods = append(d.Periods, objIntervals(i, o)...)
	}
	return d
}

// objIntervals — недельные интервалы одного объекта. Интервал k —
// [Start+7k, Start+7(k+1)); последний урезается до End. Целевая 1
// попадает ровно в один интервал: тот, в котором лежит delisted_at.
func objIntervals(objIdx int, o Obj) []Period {
	// float64 до деления: Duration/Duration — целочисленное деление.
	n := int(math.Ceil(float64(o.End.Sub(o.Start)) / float64(week)))
	if n < 1 {
		n = 1 // объект «живёт» меньше недели — один интервал всё равно есть
	}
	out := make([]Period, 0, n)
	for k := 0; k < n; k++ {
		ps := o.Start.AddDate(0, 0, 7*k)
		pe := o.Start.AddDate(0, 0, 7*(k+1))
		if pe.After(o.End) {
			pe = o.End
		}
		target := 0
		if o.Status == "delisted" && o.End.After(ps) && !o.End.After(pe) {
			target = 1
		}
		p := Period{Obj: objIdx, Week: k, Start: ps, End: pe, Target: target}
		p.Reductions, p.DropPct, p.DaysSince, p.Increased = priceFeaturesAt(o, ps)
		p.ValDev = valDevAt(o, ps)
		out = append(out, p)
	}
	return out
}

// priceFeaturesAt — поведение цены на момент t (начало интервала):
// используются ТОЛЬКО строки price_history с change_at <= t (ТЗ §9.2).
// «Цены ещё не было» = 0/0/0/0: на старте экспозиции эти значения
// естественным образом нулевые.
func priceFeaturesAt(o Obj, t time.Time) (int, float64, int, int) {
	i := 0
	for i < len(o.Prices) && !o.Prices[i].At.After(t) {
		i++
	}
	if i == 0 {
		return 0, 0, 0, 0
	}
	reductions, increased := 0, 0
	for j := 1; j < i; j++ {
		switch {
		case o.Prices[j].Minor < o.Prices[j-1].Minor:
			reductions++
		case o.Prices[j].Minor > o.Prices[j-1].Minor:
			increased = 1
		}
	}
	first, last := o.Prices[0].Minor, o.Prices[i-1].Minor
	dropPct := 0.0
	if first > 0 && last < first {
		dropPct = 100 * float64(first-last) / float64(first)
	}
	daysSince := int(math.Floor(t.Sub(o.Prices[i-1].At).Hours() / 24))
	return reductions, dropPct, daysSince, increased
}

// valDevAt — последняя price_deviation из valuations на момент начала
// интервала (computed_at <= t); nil, если оценка ещё не была
// вычислена (для реальных данных этапа 5 — пока всегда nil: модель
// отклонена, price_deviation все NULL).
func valDevAt(o Obj, t time.Time) *float64 {
	for i := len(o.ValDevs) - 1; i >= 0; i-- {
		if !o.ValDevs[i].At.After(t) {
			d := o.ValDevs[i].Deviation
			return &d
		}
	}
	return nil
}

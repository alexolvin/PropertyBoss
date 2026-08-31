package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ParseSpec — разбор списка значений «a-b,c-d,…» (диапазоны включительные)
// в отсортированный уникальный список; все значения обязаны лежать в
// [min, max]. Используется init-windows (-dow 0-6, -hours 8-20).
func ParseSpec(spec string, min, max int) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var lo, hi int
		if idx := strings.IndexByte(part, '-'); idx >= 0 {
			var err error
			lo, err = strconv.Atoi(strings.TrimSpace(part[:idx]))
			if err != nil {
				return nil, fmt.Errorf("spec %q: диапазон %q: %w", spec, part, err)
			}
			hi, err = strconv.Atoi(strings.TrimSpace(part[idx+1:]))
			if err != nil {
				return nil, fmt.Errorf("spec %q: диапазон %q: %w", spec, part, err)
			}
			if lo > hi {
				return nil, fmt.Errorf("spec %q: диапазон %q: начало больше конца", spec, part)
			}
		} else {
			var err error
			lo, err = strconv.Atoi(part) // «=», не «:=» — иначе тень, lo останется 0
			if err != nil {
				return nil, fmt.Errorf("spec %q: число %q: %w", spec, part, err)
			}
			hi = lo
		}
		if lo < min || hi > max {
			return nil, fmt.Errorf("spec %q: значения вне диапазона [%d, %d]", spec, min, max)
		}
		for v := lo; v <= hi; v++ {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("spec %q: пусто", spec)
	}
	sort.Ints(out)
	return out, nil
}

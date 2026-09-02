// Детектор языка оригинала описания (этап 10, ТЗ §11).
//
// ТЗ: «language_original определяется детектором языка, а не по стране».
// Сканер (коннектор) кладёт в language_original языковую константу
// источника (для reality.bazos.cz — «cs»); переводчик уточняет её по
// ТЕКСТУ описания. Важно: на чешском сайте может быть объявление на
// английском, и язык оригинала не равен стране объекта.
//
// Метод (прозрачный, без подгонки под тестовые выборки):
//  1. Скрипт: кириллица → «ru» (в целевом наборе только русский
//     кириллический). Детерминированно.
//  2. Латиница: характерные (почти исключительные для языка) буквы и
//     словосочетания — сильный сигнал + стандартные распределения частот
//     букв (открытые лингвистические данные) — ослабленный сигнал и
//     разбиение ничьих.
//
// Честная неопределённость: при коротком тексте или слабом/неразличимом
// сигнале уверенность ниже порога — тогда language_original НЕ
// переопределяется (не гадать вместо определения).
package translate

import (
	"strings"
	"unicode"
)

// Detector — определяет язык текста. tag — 2-буквенный ISO-639-1;
// confidence — уверенность в [0,1] (0 — не определён).
type Detector interface {
	Detect(text string) (tag string, confidence float64)
}

// minDetectLetters — сколько букв (unicode.IsLetter) нужно, чтобы
// определять язык. Меньше — ( "", 0 ). Допущение исполнителя: 20 —
// короче по сути не по чему судить (методологическая константа
// классификатора, не операционный порог — см. отчёт этапа).
const minDetectLetters = 20

// detectConfThreshold — ниже этой уверенности язык считаем неопределённым
// и language_original не трогаем. Допущение исполнителя: 0.35 (метод.
// константа: разделяет «чёткий сигнал» от «шума»).
const detectConfThreshold = 0.35

// cyrillicRatio — доля кириллицы, при которой текст считаем русским.
const cyrillicRatio = 0.5

// strongMarkers — почти исключительные для языка буквы/граммемы: их
// наличие — сильный сигнал (стандартный лингвистический факт).
var strongMarkers = map[string][]string{
	"cs": {"ř", "ť", "ň", "ě", "ů", "ď"},
	"it": {"zione", "amento", "imento", "gli "},
	"nl": {"ë", "ij", "woon", "appartement", "geen"},
	"en": {"the ", "ing", "tion", "and "},
}

// weakMarkers — характерные, но не исключительные; ослабленный сигнал.
var weakMarkers = map[string][]string{
	"cs": {"č", "š", "ž"},
	"it": {"à", "ù", "è", "ì", "ò"},
	"nl": {" het ", " een ", "sch"},
	"en": {"house", "sale"},
}

// letterFreq — стандартные пропорции частоты 26 букв латиницы (открытые
// данные по языкам, в порядке a..z). Ослабленный сигнал: сам по себе не
// разделяет близкие языки (все латиницей), но даёт устойчивый вклад и
// решает случаи без маркеров.
var letterFreq = map[string][26]float64{
	"en": {0.0817, 0.0149, 0.0278, 0.0425, 0.1270, 0.0223, 0.0202, 0.0609, 0.0697, 0.0015, 0.0077, 0.0402, 0.0241, 0.0674, 0.0751, 0.0192, 0.0010, 0.0599, 0.0632, 0.0906, 0.0276, 0.0098, 0.0236, 0.0015, 0.0197, 0.0007},
	"it": {0.1148, 0.0175, 0.0396, 0.0366, 0.1192, 0.0135, 0.0201, 0.0111, 0.0873, 0.0007, 0.0001, 0.0526, 0.0266, 0.0460, 0.0763, 0.0295, 0.0087, 0.0443, 0.0341, 0.0391, 0.0321, 0.0248, 0.0001, 0.0001, 0.0001, 0.0099},
	"nl": {0.1360, 0.0200, 0.0230, 0.0520, 0.1910, 0.0090, 0.0370, 0.0320, 0.1280, 0.0120, 0.0100, 0.0430, 0.0330, 0.1890, 0.1190, 0.0200, 0.0007, 0.0630, 0.0670, 0.0870, 0.0620, 0.0290, 0.0180, 0.0010, 0.0030, 0.0060},
	"cs": {0.0750, 0.0190, 0.0210, 0.0330, 0.0810, 0.0120, 0.0120, 0.0180, 0.0600, 0.0130, 0.0240, 0.0460, 0.0270, 0.0640, 0.1090, 0.0230, 0.0000, 0.0370, 0.0550, 0.0380, 0.0340, 0.0420, 0.0030, 0.0010, 0.0110, 0.0200},
}

// statDetector — реализация Detector.
type statDetector struct{}

// NewDetector — детектор языка оригинала.
func NewDetector() Detector { return statDetector{} }

// Detect — см. интерфейс.
func (statDetector) Detect(text string) (string, float64) {
	var letters, cyrillic int
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if r >= 0x0400 && r <= 0x04FF {
				cyrillic++
			}
		}
	}
	if letters < minDetectLetters {
		return "", 0
	}
	// 1. Скрипт: кириллица → ru (детерминированно).
	if cyrillic > 0 && float64(cyrillic)/float64(letters) >= cyrillicRatio {
		conf := float64(cyrillic) / float64(letters)
		return "ru", conf
	}
	return detectLatin(text)
}

// detectLatin — латиница: сильный сигнал (strong-маркеры) + ослабленный
// (weak-маркеры + профиль частот). Возвращает язык и уверенность.
func detectLatin(text string) (string, float64) {
	// Границы слов: маркеры вида "the " / " het " срабатывают и в начале
	// текста, и после знаков препинания.
	lower := " " + strings.ToLower(text) + " "

	// Профиль букв текста (только базовые a–z), пропорции.
	var total int
	var freq [26]float64
	for _, r := range lower {
		if r >= 'a' && r <= 'z' {
			freq[r-'a']++
			total++
		}
	}
	for i := range freq {
		if total > 0 {
			freq[i] /= float64(total)
		}
	}

	type candidate struct {
		marker float64
		prof   float64
	}
	var cands []struct {
		lang string
		c    candidate
	}
	for lang := range strongMarkers {
		c := candidate{
			marker: 2*countMarkers(lower, strongMarkers[lang]) +
				countMarkers(lower, weakMarkers[lang]),
		}
		// Ослабленный профиль: коэффициент симиларности
		// Szymkiewicz–Simpson (сумма min пропорций), [0,1].
		ref := letterFreq[lang]
		overlap := 0.0
		for i := 0; i < 26; i++ {
			if freq[i] < ref[i] {
				overlap += freq[i]
			} else {
				overlap += ref[i]
			}
		}
		c.prof = overlap
		cands = append(cands, struct {
			lang string
			c    candidate
		}{lang, c})
	}

	// Итог: маркеры доминируют, профиль — для ничьих и случая без маркеров.
	best, second, bestLang := 0.0, 0.0, ""
	for _, cd := range cands {
		s := cd.c.marker + 0.3*cd.c.prof
		if s > best {
			second, best = best, s
			bestLang = cd.lang
		} else if s > second {
			second = s
		}
	}
	if best <= 0 {
		return "", 0
	}
	// Уверенность: доля выигрыша лучшего над вторым (0 при равенстве).
	conf := (best - second) / best
	if conf < 0 {
		conf = 0
	}
	return bestLang, conf
}

// countMarkers — суммарное число вхождений всех маркеров в lower.
func countMarkers(lower string, markers []string) float64 {
	var n float64
	for _, m := range markers {
		n += float64(strings.Count(lower, m))
	}
	return n
}

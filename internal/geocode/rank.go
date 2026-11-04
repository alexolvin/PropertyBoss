package geocode

// Rank — порядок уровней уверенности ТЗ v4 §4.5:
// строение > улица > населённый пункт > индекс > зона.
// Неизвестный / пустой уровень — ниже всех (не догадываемся).
func Rank(conf string) int {
	switch conf {
	case "zone":
		return 0
	case "postcode":
		return 1
	case "municipality":
		return 2
	case "street":
		return 3
	case "building":
		return 4
	default:
		return -1
	}
}

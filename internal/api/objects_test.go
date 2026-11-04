package api

import "testing"

// TestAttachCoverNilRow — регрессия «внутренняя ошибка» /api/objects
// (09.09): объект без фото → LEFT JOIN LATERAL cov пуст → все cov.*
// NULL. Раньше cov.ord сканировался в не-pointer int16 и 500-ил всю
// страницу (317 активных объектов без фото).
func TestAttachCoverNilRow(t *testing.T) {
	var o objectOut
	attachCover(&o, nil, nil, nil, nil)
	if o.Cover != nil {
		t.Fatalf("объект без фото: Cover должен быть nil, got %+v", o.Cover)
	}

	ord := int16(2)
	u := "https://www.bazosreal.cz/foto.jpg"
	attachCover(&o, &ord, &u, nil, nil)
	if o.Cover == nil {
		t.Fatal("объект с фото: Cover не заполнен")
	}
	if o.Cover.Ord != 2 || o.Cover.OriginalURL != u {
		t.Fatalf("Cover собран неверно: %+v", o.Cover)
	}
	if o.Cover.CardURL != nil || o.Cover.ListURL != nil {
		t.Fatalf("нескачанные фото: CardURL/ListURL должны быть nil: %+v", o.Cover)
	}
}

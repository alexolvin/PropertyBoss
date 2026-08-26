package fx

import (
	"strings"
	"testing"
	"time"
)

// Реальная форма ответа eurofxref-daily.xml (один день).
const singleDayXML = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
	<gesmes:subject>Reference rates</gesmes:subject>
	<gesmes:Sender>
		<gesmes:name>European Central Bank</gesmes:name>
	</gesmes:Sender>
	<Cube>
		<Cube time='2026-08-24'>
			<Cube currency='USD' rate='1.1664'/>
			<Cube currency='JPY' rate='185.60'/>
			<Cube currency='CZK' rate='24.100'/>
			<Cube currency='GBP' rate='0.85550'/>
		</Cube>
	</Cube>
</gesmes:Envelope>`

// Форма для диапазона from/to: по одному Cube на день.
const rangeXML = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
	<gesmes:subject>Reference rates</gesmes:subject>
	<Cube>
		<Cube time='2026-08-21'>
			<Cube currency='USD' rate='1.1600'/>
			<Cube currency='CZK' rate='24.050'/>
		</Cube>
		<Cube time='2026-08-24'>
			<Cube currency='USD' rate='1.1664'/>
			<Cube currency='CZK' rate='24.100'/>
		</Cube>
	</Cube>
</gesmes:Envelope>`

func TestParseXMLSingleDay(t *testing.T) {
	days, err := ParseXML(strings.NewReader(singleDayXML))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("ожидалось 1 день, получилось %d", len(days))
	}
	want, _ := time.Parse("2006-01-02", "2026-08-24")
	if !days[0].Date.Equal(want) {
		t.Errorf("дата = %s, ожидалось 2026-08-24", days[0].Date)
	}
	if days[0].Rates["CZK"] != "24.100" {
		t.Errorf("CZK = %q, ожидалось %q (строка, без float)", days[0].Rates["CZK"], "24.100")
	}
	if days[0].Rates["USD"] != "1.1664" {
		t.Errorf("USD = %q, ожидалось %q", days[0].Rates["USD"], "1.1664")
	}
}

func TestParseXMLRange(t *testing.T) {
	days, err := ParseXML(strings.NewReader(rangeXML))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 {
		t.Fatalf("ожидалось 2 дня, получилось %d", len(days))
	}
	if !days[0].Date.Before(days[1].Date) {
		t.Error("дни не отсортированы по возрастанию")
	}
	if days[0].Rates["CZK"] != "24.050" || days[1].Rates["CZK"] != "24.100" {
		t.Errorf("курсы CZK: %q, %q — ожидалось 24.050 / 24.100", days[0].Rates["CZK"], days[1].Rates["CZK"])
	}
	// Даты не должны пересекаться
	if days[0].Date.Equal(days[1].Date) {
		t.Error("два дня с одинаковой датой")
	}
}

func TestParseXMLNoDates(t *testing.T) {
	// Курс без даты — не молчим и не подставляем, а не возвращаем (ТЗ §0.4).
	days, err := ParseXML(strings.NewReader(`<gesmes:Envelope><Cube><Cube currency='USD' rate='1.2'/></Cube></gesmes:Envelope>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 0 {
		t.Errorf("ожидалось 0 дней, получилось %d", len(days))
	}
}

func TestParseXMLMalformed(t *testing.T) {
	if _, err := ParseXML(strings.NewReader("это не xml")); err == nil {
		t.Error("ожидалась ошибка на некорректном XML")
	}
}

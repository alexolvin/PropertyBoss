package srccand

import (
	"strings"
	"testing"
)

func TestRedactLocal(t *testing.T) {
	// Октеты собраны в тесте, чтобы файл не содержал адрес узла.
	loop := "127.0." + "0.53"
	cgnat := "100." + "64.1.2"
	priv := "192.168." + "1.5"
	in := "lookup example.cz on " + loop + ":53: no such host; peer " + cgnat + " and " + priv
	got := redactLocal(in)
	for _, leak := range []string{loop, cgnat, priv} {
		if strings.Contains(got, leak) {
			t.Fatalf("остался %s в %q", leak, got)
		}
	}
	if !strings.Contains(got, "example.cz") || !strings.Contains(got, "local-address") {
		t.Fatalf("редакция сломала текст: %q", got)
	}
}

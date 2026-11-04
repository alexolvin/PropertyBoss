package probe

import (
	"testing"
	"time"
)

func TestDecideClassBazosAlwaysConnected(t *testing.T) {
	c, r := DecideClass("bazos.cz", "direct", "should not matter", nil)
	if c != "already_connected" {
		t.Fatalf("class=%s reason=%s", c, r)
	}
}

func TestDecideClassTwoRunPendingThenConfirm(t *testing.T) {
	c, r := DecideClass("example.cz", "dead", "timeout", nil)
	if c != "pending" {
		t.Fatalf("first run: %s %s", c, r)
	}
	prev := &Prev{Class: "pending", Reason: r, ProbedAt: time.Now().Add(-2 * time.Hour)}
	c2, _ := DecideClass("example.cz", "dead", "timeout", prev)
	if c2 != "dead" {
		t.Fatalf("second run: %s", c2)
	}
}

func TestDecideClassTwoRunTooSoon(t *testing.T) {
	prev := &Prev{Class: "pending", Reason: "наблюдение 1/2 класса dead: x", ProbedAt: time.Now().Add(-10 * time.Minute)}
	c, _ := DecideClass("example.cz", "dead", "x", prev)
	if c != "pending" {
		t.Fatalf("too soon: %s", c)
	}
}

func TestDecideClassRobotsNoConfirm(t *testing.T) {
	c, _ := DecideClass("sreality.cz", "excluded_robots", "Disallow: /", nil)
	if c != "excluded_robots" {
		t.Fatalf("got %s", c)
	}
}

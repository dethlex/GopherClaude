package hello

import (
	"strings"
	"testing"
)

const (
	id    = "0123456789abcdef0123456789abcdef"
	agent = "fedcba9876543210fedcba9876543210"
	tok   = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
)

func TestLine(t *testing.T) {
	if got := Line(id, "v1-dirty", tok); got != "HELLO "+id+" v1-dirty "+tok+"\n" {
		t.Fatalf("%q", got)
	}
	// An empty version still yields four fields on the wire.
	if got := Line(id, "", tok); got != "HELLO "+id+" dev "+tok+"\n" {
		t.Fatalf("%q", got)
	}
	// Spaces in a version would break the four-field format.
	if got := Line(id, "v1 dirty", tok); strings.Count(got, " ") != 3 {
		t.Fatalf("%q", got)
	}
}

func TestParseWelcome(t *testing.T) {
	if a, ok := ParseWelcome("WELCOME " + agent + "\n"); !ok || a != agent {
		t.Fatalf("%q %v", a, ok)
	}
	if a, ok := ParseWelcome("WELCOME " + agent + "\r\n"); !ok || a != agent {
		t.Fatalf("crlf: %q %v", a, ok)
	}
	for _, bad := range []string{"", "WELCOME", "WELCOME short", "HELLO " + agent, "WELCOME " + agent + " extra", "CC9|1|0"} {
		if _, ok := ParseWelcome(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

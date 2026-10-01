package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dethlex/GopherClaude/internal/infra/display"
)

func TestHexAndCodeValidation(t *testing.T) {
	if !IsHex(devA, IDLen) || IsHex(devA[:31], IDLen) || IsHex("0123456789ABCDEF0123456789ABCDEF", IDLen) || IsHex(devA+"0", IDLen) {
		t.Fatal("IsHex")
	}
	if !IsCode("007123") || IsCode("12345") || IsCode("1234567") || IsCode("12a456") || IsCode("") {
		t.Fatal("IsCode")
	}
}

func TestNonceAndSignature(t *testing.T) {
	n1, n2 := NewNonce(), NewNonce()
	if !IsHex(n1, NonceLen) || n1 == n2 {
		t.Fatalf("nonces %q %q", n1, n2)
	}
	sig := DeviceSignature(secA, devA, n1)
	if !IsHex(sig, TokenLen) || sig != DeviceSignature(secA, devA, n1) {
		t.Fatal("signature must be deterministic hex")
	}
	if sig == DeviceSignature(secA, devA, n2) || sig == DeviceSignature(secB, devA, n1) || sig == DeviceSignature(secA, devB, n1) {
		t.Fatal("signature must depend on nonce, secret and device id")
	}
	if DeviceSignature("zz", devA, n1) != "" {
		t.Fatal("bad secret must yield an empty signature")
	}
	if !Equal(sig, sig) || Equal(sig, n1) {
		t.Fatal("Equal")
	}
}

func TestDeviceTokenMatchesAgentSide(t *testing.T) {
	// The agent accepts HELLO tokens computed by display.TokenFor; the Hub
	// hands the agent exactly that value at pairing time.
	if DeviceToken(secA, agX) != display.TokenFor(secA, agX) {
		t.Fatal("hub and agent disagree on the device token")
	}
	if DeviceToken("nothex", agX) != "" {
		t.Fatal("bad secret must yield an empty token")
	}
}

func TestBearerToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if BearerToken(r) != "" {
		t.Fatal("no header")
	}
	r.Header.Set("Authorization", "Bearer "+tokX)
	if BearerToken(r) != tokX {
		t.Fatal("bearer")
	}
	r.Header.Set("Authorization", "Basic "+tokX)
	if BearerToken(r) != "" {
		t.Fatal("scheme")
	}
	r.Header.Set("Authorization", "Bearer short")
	if BearerToken(r) != "" {
		t.Fatal("length")
	}
}

func TestPublicIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "10.0.0.7:4567"
	if PublicIP(r) != "10.0.0.7" {
		t.Fatalf("%q", PublicIP(r))
	}
	r.Header.Set("X-Forwarded-For", " 203.0.113.5 , 10.0.0.1")
	if PublicIP(r) != "203.0.113.5" {
		t.Fatalf("%q", PublicIP(r))
	}
	r.Header.Set("X-Forwarded-For", "2001:db8::1")
	if PublicIP(r) != "2001:db8::1" {
		t.Fatalf("%q", PublicIP(r))
	}
	r.Header.Set("X-Forwarded-For", "garbage")
	if PublicIP(r) != "" {
		t.Fatalf("an unparsable forwarded address must not be trusted: %q", PublicIP(r))
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		if !l.Allow(agX, t0.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("attempt %d denied", i)
		}
	}
	if l.Allow(agX, t0.Add(10*time.Second)) {
		t.Fatal("sixth attempt within the window allowed")
	}
	if !l.Allow(agY, t0.Add(10*time.Second)) {
		t.Fatal("another key must have its own budget")
	}
	if !l.Allow(agX, t0.Add(61*time.Second)) {
		t.Fatal("the first attempt has left the window")
	}
}

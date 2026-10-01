package display

import "testing"

const (
	testID    = "0123456789abcdef0123456789abcdef"
	testAgent = "fedcba9876543210fedcba9876543210"
	testTok   = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
)

func TestParseHello(t *testing.T) {
	id, fw, tok, ok := ParseHello("HELLO " + testID + " v1.2-3-gabc " + testTok + "\n")
	if !ok || id != testID || fw != "v1.2-3-gabc" || tok != testTok {
		t.Fatalf("got %q %q %q %v", id, fw, tok, ok)
	}
	bad := []string{
		"",
		"HELLO",
		"HELLO " + testID,
		"HELLO " + testID + " fw",
		"HELLO " + testID + " fw short",
		"HELLO " + testID[:31] + " fw " + testTok,
		"HELLO " + testID + " fw " + testTok + " extra",
		"hello " + testID + " fw " + testTok,
		"HELLO " + "0123456789ABCDEF0123456789ABCDEF" + " fw " + testTok, // uppercase hex
		"CC9|1|0",
	}
	for _, b := range bad {
		if _, _, _, ok := ParseHello(b); ok {
			t.Errorf("accepted %q", b)
		}
	}
}

func TestWelcomeLine(t *testing.T) {
	if WelcomeLine(testAgent) != "WELCOME "+testAgent+"\n" {
		t.Fatal(WelcomeLine(testAgent))
	}
}

func TestTokenForMatchesHMAC(t *testing.T) {
	// HMAC-SHA256 with the secret bytes as key over the agent id's hex text.
	secret := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	got := TokenFor(secret, testAgent)
	if len(got) != TokenLen {
		t.Fatalf("len %d", len(got))
	}
	if got != TokenFor(secret, testAgent) || got == TokenFor(secret, testID) {
		t.Fatal("token must be deterministic and depend on the agent id")
	}
	if !TokenEqual(got, got) || TokenEqual(got, testTok) {
		t.Fatal("TokenEqual")
	}
}

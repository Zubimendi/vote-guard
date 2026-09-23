package votes

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestHashTokenDeterministic(t *testing.T) {
	a := HashToken("abc")
	b := HashToken("abc")
	if a != b {
		t.Fatalf("hash not deterministic")
	}
	sum := sha256.Sum256([]byte("abc"))
	want := hex.EncodeToString(sum[:])
	if a != want {
		t.Fatalf("unexpected hash")
	}
}

func TestTokenEntropyFloor(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		tok := hex.EncodeToString(b)
		if len(tok) < 64 {
			t.Fatalf("token too short: %d", len(tok))
		}
		if _, ok := seen[tok]; ok {
			t.Fatalf("duplicate token in small sample")
		}
		seen[tok] = struct{}{}
	}
}

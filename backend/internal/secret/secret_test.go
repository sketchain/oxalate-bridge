package secret

import (
	"strings"
	"testing"
)

func TestBoxRoundTrip(t *testing.T) {
	box, err := NewBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed := box.Seal("sk-abc")
	got, err := box.Open(sealed)
	if err != nil || got != "sk-abc" {
		t.Fatalf("open = %q, %v", got, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("tampered value opened")
	}
}

func TestPassword(t *testing.T) {
	h := HashPassword("hunter2")
	if !CheckPassword(h, "hunter2") || CheckPassword(h, "hunter3") || CheckPassword("junk", "hunter2") {
		t.Fatal("password check wrong")
	}
}

func TestAPIKey(t *testing.T) {
	k := NewAPIKey()
	if !strings.HasPrefix(k, "sk-") || len(k) != 51 {
		t.Fatalf("key %q", k)
	}
	if p := KeyPrefix(k); !strings.HasPrefix(p, "sk-") || !strings.HasSuffix(p, k[len(k)-4:]) {
		t.Fatalf("prefix %q", p)
	}
}

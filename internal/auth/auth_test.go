package auth

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("unexpected hash format: %s", h)
	}
	if strings.Contains(h, "correct-horse") {
		t.Error("hash contains the password")
	}
	if !CheckPassword(h, "correct-horse-battery") {
		t.Error("the right password was refused")
	}
	if CheckPassword(h, "correct-horse-batterx") || CheckPassword(h, "") {
		t.Error("a wrong password was accepted")
	}
	h2, _ := HashPassword("correct-horse-battery")
	if h == h2 {
		t.Error("two hashes of one password are identical: the salt is not random")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$!!$!!", "$bcrypt$x$y$z$w"} {
		if CheckPassword(bad, "x") {
			t.Errorf("malformed hash %q was accepted", bad)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin, referer string
		want            bool
	}{
		{"http://example.com", "", true},
		{"", "http://example.com/login", true},
		{"https://evil.example", "", false},
		{"", "", false},
		{"null", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "http://example.com/login", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.referer != "" {
			r.Header.Set("Referer", c.referer)
		}
		if got := SameOrigin(r); got != c.want {
			t.Errorf("origin %q referer %q: got %v", c.origin, c.referer, got)
		}
	}
}

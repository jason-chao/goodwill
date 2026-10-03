package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.toml")
	os.WriteFile(path, []byte(`
[server]
public_listen = "0.0.0.0:9000"
trusted_proxies = ["127.0.0.1", "10.0.0.0/8"]

[database]
path = "/var/lib/goodwill/goodwill.db"

[limits]
events_per_minute = 120
`), 0o600)
	t.Setenv("GOODWILL_ADMIN_LISTEN", "127.0.0.1:9001")
	t.Setenv("GOODWILL_EVENTS_PER_MINUTE", "300")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.PublicListen != "0.0.0.0:9000" || cfg.Database.Path != "/var/lib/goodwill/goodwill.db" {
		t.Errorf("file values not applied: %+v", cfg)
	}
	if cfg.Server.AdminListen != "127.0.0.1:9001" || cfg.Limits.EventsPerMinute != 300 {
		t.Errorf("environment should override the file: %+v", cfg)
	}
	if cfg.Server.ClientIPHeader != "X-Forwarded-For" || cfg.Limits.MaxBodyBytes != 64<<10 {
		t.Errorf("defaults lost: %+v", cfg)
	}

	os.WriteFile(path, []byte("[server]\npublic_listn = \"x\"\n"), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("a misspelt setting should be an error, not silently ignored")
	}
	os.WriteFile(path, []byte("[server]\ntrusted_proxies = [\"not-an-address\"]\n"), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("a bad proxy address should be an error")
	}
	t.Setenv("GOODWILL_ADMIN_LISTEN", "127.0.0.1:8080")
	if _, err := Load(""); err == nil {
		t.Error("both listeners on one address should be an error")
	}
}

func TestDefaultsAreLoopback(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{cfg.Server.PublicListen, cfg.Server.AdminListen} {
		ap, err := netip.ParseAddrPort(l)
		if err != nil || !ap.Addr().IsLoopback() {
			t.Errorf("default listener %q is not on loopback", l)
		}
	}
}

func TestPrefixes(t *testing.T) {
	ps, err := ParsePrefixes([]string{"192.0.2.7", " 10.0.0.0/8 ", "2001:db8::/32", ""})
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{
		"192.0.2.7": true, "192.0.2.8": false, "10.200.1.1": true, "2001:db8:1::1": true, "2001:db9::1": false,
		"::ffff:192.0.2.7": true,
	} {
		if got := Contains(ps, netip.MustParseAddr(addr)); got != want {
			t.Errorf("Contains(%s) = %v, want %v", addr, got, want)
		}
	}
	if _, err := ParsePrefixes([]string{"example.com"}); err == nil {
		t.Error("a hostname is not an address")
	}
}

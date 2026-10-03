// Package config loads settings from an optional TOML file, with
// GOODWILL_* environment variables taking precedence.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server   Server   `toml:"server"`
	Database Database `toml:"database"`
	Geo      Geo      `toml:"geo"`
	Limits   Limits   `toml:"limits"`
}

type Server struct {
	// PublicListen serves the tracker script and receives events.
	PublicListen string `toml:"public_listen"`
	// AdminListen serves the dashboard. Keep it on loopback or a private
	// network.
	AdminListen string `toml:"admin_listen"`
	// TrustedProxies lists addresses or CIDR ranges of reverse proxies whose
	// client address header is believed.
	TrustedProxies []string `toml:"trusted_proxies"`
	// ClientIPHeader is the header a trusted proxy puts the visitor's
	// address in.
	ClientIPHeader string `toml:"client_ip_header"`
	// PublicURL is the address browsers reach PublicListen at, for example
	// "https://stats.example.com". It is shown in the tracking snippet.
	PublicURL string `toml:"public_url"`
	// SecureCookies marks the dashboard session cookie HTTPS-only.
	SecureCookies bool `toml:"secure_cookies"`
}

type Database struct {
	Path string `toml:"path"`
}

type Geo struct {
	// Path is an optional .mmdb location database. It replaces the built-in
	// country database.
	Path string `toml:"path"`
	// Attribution is shown in the dashboard footer when Path is set, for
	// databases whose licence asks for a credit.
	Attribution string `toml:"attribution"`
}

type Limits struct {
	// EventsPerMinute caps events accepted from one address.
	EventsPerMinute int `toml:"events_per_minute"`
	// MaxBodyBytes caps the size of one event request.
	MaxBodyBytes int64 `toml:"max_body_bytes"`
}

// Default returns the settings used when nothing is configured.
func Default() Config {
	return Config{
		Server: Server{
			PublicListen:   "127.0.0.1:8080",
			AdminListen:    "127.0.0.1:8081",
			ClientIPHeader: "X-Forwarded-For",
		},
		Database: Database{Path: "goodwill.db"},
		Limits:   Limits{EventsPerMinute: 600, MaxBodyBytes: 64 << 10},
	}
}

// Load reads path (if not empty) over the defaults, then applies environment
// variables.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		md, err := toml.DecodeFile(path, &cfg)
		if err != nil {
			return cfg, fmt.Errorf("config %s: %w", path, err)
		}
		if extra := md.Undecoded(); len(extra) > 0 {
			return cfg, fmt.Errorf("config %s: unknown setting %q", path, extra[0].String())
		}
	}
	if err := cfg.applyEnv(os.Getenv); err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

func (c *Config) applyEnv(get func(string) string) error {
	str := func(name string, dst *string) {
		if v := get(name); v != "" {
			*dst = v
		}
	}
	str("GOODWILL_PUBLIC_LISTEN", &c.Server.PublicListen)
	str("GOODWILL_ADMIN_LISTEN", &c.Server.AdminListen)
	str("GOODWILL_CLIENT_IP_HEADER", &c.Server.ClientIPHeader)
	str("GOODWILL_PUBLIC_URL", &c.Server.PublicURL)
	str("GOODWILL_DATABASE_PATH", &c.Database.Path)
	str("GOODWILL_GEO_PATH", &c.Geo.Path)
	str("GOODWILL_GEO_ATTRIBUTION", &c.Geo.Attribution)
	if v := get("GOODWILL_TRUSTED_PROXIES"); v != "" {
		c.Server.TrustedProxies = splitList(v)
	}
	if v := get("GOODWILL_SECURE_COOKIES"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("GOODWILL_SECURE_COOKIES: %w", err)
		}
		c.Server.SecureCookies = b
	}
	if v := get("GOODWILL_EVENTS_PER_MINUTE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("GOODWILL_EVENTS_PER_MINUTE: %w", err)
		}
		c.Limits.EventsPerMinute = n
	}
	if v := get("GOODWILL_MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("GOODWILL_MAX_BODY_BYTES: %w", err)
		}
		c.Limits.MaxBodyBytes = n
	}
	return nil
}

func (c *Config) validate() error {
	if c.Server.PublicListen == "" || c.Server.AdminListen == "" {
		return errors.New("server.public_listen and server.admin_listen are required")
	}
	if c.Server.PublicListen == c.Server.AdminListen {
		return errors.New("server.public_listen and server.admin_listen must differ")
	}
	if c.Database.Path == "" {
		return errors.New("database.path is required")
	}
	if c.Limits.MaxBodyBytes <= 0 {
		return errors.New("limits.max_body_bytes must be positive")
	}
	if _, err := ParsePrefixes(c.Server.TrustedProxies); err != nil {
		return fmt.Errorf("server.trusted_proxies: %w", err)
	}
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParsePrefixes parses a list of addresses and CIDR ranges.
func ParsePrefixes(list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not an address or CIDR range", s)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// Contains reports whether addr is inside any of the prefixes.
func Contains(prefixes []netip.Prefix, addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

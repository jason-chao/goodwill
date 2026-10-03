package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Site is one tracked website or app.
type Site struct {
	ID            int64
	PublicID      string
	Name          string
	Domains       []string
	Timezone      string
	SaltInterval  string
	Settings      Settings
	IngestKeyHash []byte
	CreatedAt     int64

	loc *time.Location
}

// Settings are the per-site collection choices.
type Settings struct {
	Collect Collect `json:"collect"`
	// IPMode is "full" (address and user agent are hashed) or "masked" (only a
	// truncated address is hashed and the user agent is left out).
	IPMode string `json:"ip_mode"`
	// IgnoreIPs lists addresses or CIDR ranges whose events are discarded.
	IgnoreIPs []string `json:"ignore_ips"`
	// TrustedSources lists addresses or CIDR ranges of your own servers, which
	// may send events on behalf of visitors without an ingest key.
	TrustedSources []string `json:"trusted_sources"`
	// KeepQuery lists query-string keys kept as part of the page path.
	KeepQuery []string `json:"keep_query"`
}

// Collect switches individual data points on or off.
type Collect struct {
	Referrer    bool `json:"referrer"`
	UserAgent   bool `json:"user_agent"`
	Screen      bool `json:"screen"`
	Language    bool `json:"language"`
	UTM         bool `json:"utm"`
	Props       bool `json:"props"`
	Identify    bool `json:"identify"`
	Performance bool `json:"performance"`
	// Location is "none", "country", "region" or "city".
	Location string `json:"location"`
}

// DefaultSettings collects everything except visitor identification, with
// location limited to country.
func DefaultSettings() Settings {
	return Settings{
		Collect: Collect{
			Referrer: true, UserAgent: true, Screen: true, Language: true,
			UTM: true, Props: true, Identify: false, Performance: true,
			Location: "country",
		},
		IPMode: "full",
	}
}

// Location returns the site's timezone, falling back to UTC.
func (s *Site) Location() *time.Location {
	if s.loc == nil {
		return time.UTC
	}
	return s.loc
}

var intervalRe = regexp.MustCompile(`^(day|week|month|[1-9][0-9]{0,2}d)$`)

// ValidSaltInterval reports whether v is day, week, month or "<n>d".
func ValidSaltInterval(v string) bool { return intervalRe.MatchString(v) }

func (s *Site) validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("site name is required")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("unknown timezone %q", s.Timezone)
	}
	if !ValidSaltInterval(s.SaltInterval) {
		return fmt.Errorf("salt interval %q: want day, week, month or <n>d", s.SaltInterval)
	}
	switch s.Settings.Collect.Location {
	case "none", "country", "region", "city":
	default:
		return fmt.Errorf("location %q: want none, country, region or city", s.Settings.Collect.Location)
	}
	switch s.Settings.IPMode {
	case "full", "masked":
	default:
		return fmt.Errorf("ip mode %q: want full or masked", s.Settings.IPMode)
	}
	return nil
}

// NewUUID returns a random version 4 UUID.
func NewUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// CreateSite inserts a site, filling in its public ID.
func (s *Store) CreateSite(site *Site) error {
	if err := site.validate(); err != nil {
		return err
	}
	if site.PublicID == "" {
		site.PublicID = NewUUID()
	}
	if site.Domains == nil {
		site.Domains = []string{}
	}
	domains, _ := json.Marshal(site.Domains)
	settings, _ := json.Marshal(site.Settings)
	site.CreatedAt = time.Now().Unix()
	res, err := s.w.Exec(`insert into sites (public_id, name, domains, timezone, salt_interval, settings, created_at)
		values (?, ?, ?, ?, ?, ?, ?)`,
		site.PublicID, site.Name, string(domains), site.Timezone, site.SaltInterval, string(settings), site.CreatedAt)
	if err != nil {
		return err
	}
	site.ID, _ = res.LastInsertId()
	s.invalidateSites()
	return nil
}

// UpdateSite saves a site's name, domains, timezone, salt interval and settings.
func (s *Store) UpdateSite(site *Site) error {
	if err := site.validate(); err != nil {
		return err
	}
	domains, _ := json.Marshal(site.Domains)
	settings, _ := json.Marshal(site.Settings)
	_, err := s.w.Exec(`update sites set name = ?, domains = ?, timezone = ?, salt_interval = ?, settings = ? where id = ?`,
		site.Name, string(domains), site.Timezone, site.SaltInterval, string(settings), site.ID)
	s.invalidateSites()
	return err
}

// NewIngestKey generates, stores and returns a new ingest key for the site.
// Only a hash is stored, so the key cannot be shown again.
func (s *Store) NewIngestKey(siteID int64) (string, error) {
	var b [24]byte
	rand.Read(b[:])
	key := "gw_" + hex.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(key))
	_, err := s.w.Exec(`update sites set ingest_key_hash = ? where id = ?`, sum[:], siteID)
	s.invalidateSites()
	return key, err
}

// CheckIngestKey reports whether key is the site's ingest key.
func (site *Site) CheckIngestKey(key string) bool {
	if key == "" || len(site.IngestKeyHash) == 0 {
		return false
	}
	sum := sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare(sum[:], site.IngestKeyHash) == 1
}

const siteColumns = `id, public_id, name, domains, timezone, salt_interval, settings, ingest_key_hash, created_at`

func scanSite(row interface{ Scan(...any) error }) (*Site, error) {
	var site Site
	var domains, settings string
	if err := row.Scan(&site.ID, &site.PublicID, &site.Name, &domains, &site.Timezone,
		&site.SaltInterval, &settings, &site.IngestKeyHash, &site.CreatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(domains), &site.Domains)
	site.Settings = DefaultSettings()
	json.Unmarshal([]byte(settings), &site.Settings)
	if loc, err := time.LoadLocation(site.Timezone); err == nil {
		site.loc = loc
	}
	return &site, nil
}

// Sites lists all sites ordered by name.
func (s *Store) Sites() ([]*Site, error) {
	rows, err := s.r.Query(`select ` + siteColumns + ` from sites order by name collate nocase, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Site
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

// Site returns a site by numeric ID, or nil if there is none.
func (s *Store) Site(id int64) (*Site, error) {
	site, err := scanSite(s.r.QueryRow(`select `+siteColumns+` from sites where id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return site, err
}

// siteCacheTTL bounds how long a change made by another process (the command
// line, while the server runs) takes to reach the ingest path.
const siteCacheTTL = 15

// SiteByPublicID returns the site with the given public ID from a short-lived
// cache, or nil if there is none. The returned value must not be modified.
func (s *Store) SiteByPublicID(publicID string) (*Site, error) {
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	now := time.Now().Unix()
	if s.siteCache == nil || now-s.siteLoaded >= siteCacheTTL {
		sites, err := s.Sites()
		if err != nil {
			return nil, err
		}
		cache := make(map[string]*Site, len(sites))
		for _, site := range sites {
			cache[site.PublicID] = site
		}
		s.siteCache, s.siteLoaded = cache, now
	}
	return s.siteCache[strings.ToLower(publicID)], nil
}

func (s *Store) invalidateSites() {
	s.siteMu.Lock()
	s.siteCache = nil
	s.siteMu.Unlock()
}

// Salt is the secret mixed into visitor hashes for one site.
type Salt struct {
	Value    []byte
	Interval string
	Period   string
}

// GetSalt returns the site's current salt, or nil if none has been made yet.
func (s *Store) GetSalt(siteID int64) (*Salt, error) {
	var salt Salt
	err := s.r.QueryRow(`select salt, interval, period from salts where site_id = ?`, siteID).
		Scan(&salt.Value, &salt.Interval, &salt.Period)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &salt, err
}

// ReplaceSalt overwrites the site's salt. The previous value is gone for good.
func (s *Store) ReplaceSalt(siteID int64, salt *Salt) error {
	_, err := s.w.Exec(`insert into salts (site_id, salt, interval, period, rotated_at) values (?, ?, ?, ?, ?)
		on conflict (site_id) do update set salt = excluded.salt, interval = excluded.interval,
			period = excluded.period, rotated_at = excluded.rotated_at`,
		siteID, salt.Value, salt.Interval, salt.Period, time.Now().Unix())
	return err
}

// User is a dashboard account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// UpsertUser creates the user or replaces its password hash.
func (s *Store) UpsertUser(username, passwordHash string) error {
	_, err := s.w.Exec(`insert into users (username, password_hash, created_at) values (?, ?, ?)
		on conflict (username) do update set password_hash = excluded.password_hash`,
		username, passwordHash, time.Now().Unix())
	return err
}

// UserByName returns the named user, or nil.
func (s *Store) UserByName(username string) (*User, error) {
	var u User
	err := s.r.QueryRow(`select id, username, password_hash from users where username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

// AuthSession is a signed-in dashboard session.
type AuthSession struct {
	UserID   int64
	Username string
	CSRF     string
}

func (s *Store) CreateAuthSession(tokenHash []byte, userID int64, csrf string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.w.Exec(`insert into auth_sessions (token_hash, user_id, csrf, created_at, expires_at) values (?, ?, ?, ?, ?)`,
		tokenHash, userID, csrf, now.Unix(), now.Add(ttl).Unix())
	return err
}

// AuthSession returns the unexpired session for tokenHash, or nil.
func (s *Store) AuthSession(tokenHash []byte) (*AuthSession, error) {
	var a AuthSession
	err := s.r.QueryRow(`select u.id, u.username, a.csrf from auth_sessions a join users u on u.id = a.user_id
		where a.token_hash = ? and a.expires_at > ?`, tokenHash, time.Now().Unix()).
		Scan(&a.UserID, &a.Username, &a.CSRF)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

func (s *Store) DeleteAuthSession(tokenHash []byte) error {
	_, err := s.w.Exec(`delete from auth_sessions where token_hash = ?`, tokenHash)
	return err
}

// PruneAuthSessions removes expired sessions.
func (s *Store) PruneAuthSessions() error {
	_, err := s.w.Exec(`delete from auth_sessions where expires_at <= ?`, time.Now().Unix())
	return err
}

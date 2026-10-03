// Package identity turns a request into a visitor hash and a visit ID
// without keeping the address or user agent it was derived from.
//
// A visitor hash is an HMAC of the address and user agent under a random
// per-site salt. The salt is replaced at the end of each interval (a day by
// default) and the old one is discarded, so hashes from an earlier interval
// cannot be recomputed or linked to later ones.
package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jason-chao/goodwill/internal/store"
)

// VisitTimeout is how long a visitor can be idle before their next event
// starts a new visit.
const VisitTimeout = 30 * 60

// ErrBeforeSalt is returned for events dated before the current salt came
// into use: the salt that would have hashed them no longer exists.
var ErrBeforeSalt = errors.New("timestamp is before the current salt interval")

// SaltStore is the persistence the manager needs.
type SaltStore interface {
	GetSalt(siteID int64) (*store.Salt, error)
	ReplaceSalt(siteID int64, salt *store.Salt) error
}

// Manager hands out visitor hashes and visit IDs.
type Manager struct {
	st SaltStore

	mu     sync.Mutex
	salts  map[int64]*cachedSalt
	visits map[visitKey]*visitState
}

type cachedSalt struct {
	value    []byte
	interval string
	period   string
	start    time.Time // first instant of the period
	end      time.Time // first instant of the next period
}

type visitKey struct {
	site    int64
	visitor [8]byte
}

type visitState struct {
	id   [8]byte
	last int64
}

func NewManager(st SaltStore) *Manager {
	return &Manager{st: st, salts: map[int64]*cachedSalt{}, visits: map[visitKey]*visitState{}}
}

// period returns the identifier, start and end of the interval containing t
// in the given location.
//
// "<n>d" intervals are counted in whole local days from 1 January 1970, so
// their boundaries do not depend on when the site was created.
func period(interval string, t time.Time, loc *time.Location) (id string, start, end time.Time) {
	t = t.In(loc)
	y, m, d := t.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
	switch {
	case interval == "week":
		back := (int(t.Weekday()) + 6) % 7 // days since Monday
		start = midnight.AddDate(0, 0, -back)
		end = start.AddDate(0, 0, 7)
	case interval == "month":
		start = time.Date(y, m, 1, 0, 0, 0, 0, loc)
		end = start.AddDate(0, 1, 0)
	default:
		n := 1
		if strings.HasSuffix(interval, "d") {
			if v, err := strconv.Atoi(strings.TrimSuffix(interval, "d")); err == nil && v > 0 {
				n = v
			}
		}
		// Day number of the local calendar date, independent of clock changes.
		dayNo := int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
		back := dayNo % n
		start = midnight.AddDate(0, 0, -back)
		end = start.AddDate(0, 0, n)
	}
	return interval + ":" + start.Format("2006-01-02"), start, end
}

// salt returns the site's salt for the instant now, rotating it if the
// interval it was made for has ended.
//
// A change to the site's interval takes effect at the next boundary of the
// interval the current salt was made under.
func (m *Manager) salt(site *store.Site, now time.Time) (*cachedSalt, error) {
	if c := m.salts[site.ID]; c != nil && now.Before(c.end) && !now.Before(c.start) {
		return c, nil
	}
	stored, err := m.st.GetSalt(site.ID)
	if err != nil {
		return nil, err
	}
	loc := site.Location()
	if stored != nil {
		id, start, end := period(stored.Interval, now, loc)
		if id == stored.Period {
			c := &cachedSalt{stored.Value, stored.Interval, stored.Period, start, end}
			m.salts[site.ID] = c
			return c, nil
		}
	}
	id, start, end := period(site.SaltInterval, now, loc)
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	if err := m.st.ReplaceSalt(site.ID, &store.Salt{Value: value, Interval: site.SaltInterval, Period: id}); err != nil {
		return nil, err
	}
	c := &cachedSalt{value, site.SaltInterval, id, start, end}
	m.salts[site.ID] = c
	// Visits hashed under the old salt can never be continued.
	for k := range m.visits {
		if k.site == site.ID {
			delete(m.visits, k)
		}
	}
	return c, nil
}

// Result is the identity assigned to one event.
type Result struct {
	Visitor  []byte
	Visit    []byte
	NewVisit bool
}

// Identify returns the visitor hash and visit ID for an event.
//
// now is the server clock and decides which salt is current. ts is the time
// of the event, which differs from now only for backdated events from a
// trusted sender; those are refused if they predate the current salt.
//
// An ID supplied through identify() is deliberately not part of the hash:
// signing in must not turn one visitor into two.
func (m *Manager) Identify(site *store.Site, addr netip.Addr, userAgent string, ts int64, now time.Time) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	c, err := m.salt(site, now)
	if err != nil {
		return Result{}, fmt.Errorf("salt: %w", err)
	}
	if ts < c.start.Unix() {
		return Result{}, ErrBeforeSalt
	}

	mac := hmac.New(sha256.New, c.value)
	fmt.Fprintf(mac, "%d\x00", site.ID)
	if site.Settings.IPMode == "masked" {
		mac.Write([]byte(MaskAddr(addr).String()))
	} else {
		mac.Write([]byte(addr.String()))
		mac.Write([]byte{0})
		mac.Write([]byte(userAgent))
	}
	sum := mac.Sum(nil)

	key := visitKey{site: site.ID}
	copy(key.visitor[:], sum[:8])

	st := m.visits[key]
	fresh := st == nil || ts-st.last > VisitTimeout || st.last-ts > VisitTimeout
	if fresh {
		st = &visitState{}
		rand.Read(st.id[:])
		m.visits[key] = st
	}
	if ts > st.last {
		st.last = ts
	}
	return Result{
		Visitor:  append([]byte(nil), key.visitor[:]...),
		Visit:    append([]byte(nil), st.id[:]...),
		NewVisit: fresh,
	}, nil
}

// MaskAddr drops the host part of an address: the last octet of IPv4, and
// everything after the first 48 bits of IPv6.
func MaskAddr(addr netip.Addr) netip.Addr {
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	p, err := addr.Prefix(bits)
	if err != nil {
		return addr
	}
	return p.Addr()
}

// Evict forgets visits that have been idle past the timeout.
func (m *Manager) Evict(now int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, st := range m.visits {
		if now-st.last > VisitTimeout {
			delete(m.visits, k)
		}
	}
}

// OpenVisits reports how many visits are being tracked.
func (m *Manager) OpenVisits() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.visits)
}

type savedVisit struct {
	Site    int64   `json:"s"`
	Visitor [8]byte `json:"v"`
	ID      [8]byte `json:"i"`
	Last    int64   `json:"l"`
}

// Snapshot serialises the open visits so that a restart does not split them.
// It holds visitor hashes and visit IDs only.
func (m *Manager) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]savedVisit, 0, len(m.visits))
	for k, st := range m.visits {
		out = append(out, savedVisit{k.site, k.visitor, st.id, st.last})
	}
	return json.Marshal(out)
}

// Restore loads a snapshot, skipping visits that have since timed out.
func (m *Manager) Restore(data []byte, now int64) error {
	var in []savedVisit
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range in {
		if now-v.Last > VisitTimeout {
			continue
		}
		m.visits[visitKey{v.Site, v.Visitor}] = &visitState{id: v.ID, last: v.Last}
	}
	return nil
}

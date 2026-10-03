package identity

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/jason-chao/goodwill/internal/store"
)

// memSalts is a SaltStore that, like the real one, holds one salt per site.
type memSalts struct {
	salts    map[int64]*store.Salt
	replaced int
}

func (m *memSalts) GetSalt(id int64) (*store.Salt, error) { return m.salts[id], nil }
func (m *memSalts) ReplaceSalt(id int64, s *store.Salt) error {
	m.salts[id] = s
	m.replaced++
	return nil
}

func testSite(t *testing.T, interval, zone string) *store.Site {
	t.Helper()
	s := &store.Site{ID: 1, Name: "t", Timezone: zone, SaltInterval: interval, Settings: store.DefaultSettings()}
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateSite(s); err != nil {
		t.Fatal(err)
	}
	site, _ := st.Site(s.ID)
	return site
}

var (
	addr = netip.MustParseAddr("192.0.2.10")
	ua   = "Mozilla/5.0 (test)"
)

func at(t *testing.T, zone, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func visitor(t *testing.T, m *Manager, site *store.Site, now time.Time) []byte {
	t.Helper()
	r, err := m.Identify(site, addr, ua, now.Unix(), now)
	if err != nil {
		t.Fatal(err)
	}
	return r.Visitor
}

func TestSaltRotatesAtIntervalBoundary(t *testing.T) {
	const zone = "America/New_York"
	cases := []struct {
		interval            string
		first, same, rotate string
	}{
		// Local midnight, not UTC midnight.
		{"day", "2026-03-10 00:05", "2026-03-10 23:55", "2026-03-11 00:01"},
		// The day the clocks go forward is 23 hours long.
		{"day", "2026-03-08 00:30", "2026-03-08 23:30", "2026-03-09 00:00"},
		// Weeks start on Monday.
		{"week", "2026-03-09 00:00", "2026-03-15 23:59", "2026-03-16 00:00"},
		{"month", "2026-01-01 00:00", "2026-01-31 23:59", "2026-02-01 00:00"},
		// Day 20522 since 1970 is 2026-03-10; 20522 % 3 == 2, so the three-day
		// block runs 8 to 10 March.
		{"3d", "2026-03-08 00:00", "2026-03-10 23:59", "2026-03-11 00:00"},
	}
	for _, c := range cases {
		t.Run(c.interval+"/"+c.first, func(t *testing.T) {
			site := testSite(t, c.interval, zone)
			ms := &memSalts{salts: map[int64]*store.Salt{}}
			m := NewManager(ms)

			v1 := visitor(t, m, site, at(t, zone, c.first))
			salt1 := append([]byte(nil), ms.salts[1].Value...)
			v2 := visitor(t, m, site, at(t, zone, c.same))
			if !bytes.Equal(v1, v2) {
				t.Fatalf("visitor changed inside one %s interval", c.interval)
			}
			if ms.replaced != 1 {
				t.Fatalf("salt replaced %d times inside one interval, want 1", ms.replaced)
			}
			v3 := visitor(t, m, site, at(t, zone, c.rotate))
			if bytes.Equal(v1, v3) {
				t.Fatalf("visitor did not change after the %s interval ended", c.interval)
			}
			if ms.replaced != 2 {
				t.Fatalf("salt replaced %d times, want 2", ms.replaced)
			}
			if bytes.Equal(salt1, ms.salts[1].Value) {
				t.Fatal("salt value did not change on rotation")
			}
			if len(ms.salts) != 1 {
				t.Fatalf("store holds %d salts, want exactly 1", len(ms.salts))
			}
		})
	}
}

func TestSaltSurvivesRestartWithinInterval(t *testing.T) {
	site := testSite(t, "day", "UTC")
	ms := &memSalts{salts: map[int64]*store.Salt{}}
	v1 := visitor(t, NewManager(ms), site, at(t, "UTC", "2026-05-01 09:00"))
	// A new manager, as after a restart, reads the stored salt.
	v2 := visitor(t, NewManager(ms), site, at(t, "UTC", "2026-05-01 18:00"))
	if !bytes.Equal(v1, v2) {
		t.Fatal("visitor changed across a restart inside one interval")
	}
	if ms.replaced != 1 {
		t.Fatalf("salt replaced %d times, want 1", ms.replaced)
	}
}

func TestIntervalChangeWaitsForBoundary(t *testing.T) {
	site := testSite(t, "week", "UTC")
	ms := &memSalts{salts: map[int64]*store.Salt{}}
	m := NewManager(ms)
	v1 := visitor(t, m, site, at(t, "UTC", "2026-03-09 10:00")) // Monday

	changed := *site
	changed.SaltInterval = "day"
	// Still inside the week the salt was made for: nothing rotates, even
	// after a restart.
	m = NewManager(ms)
	if v := visitor(t, m, &changed, at(t, "UTC", "2026-03-12 10:00")); !bytes.Equal(v, v1) {
		t.Fatal("interval change took effect before the current interval ended")
	}
	// The week ends: the new salt is daily.
	v2 := visitor(t, m, &changed, at(t, "UTC", "2026-03-16 10:00"))
	if bytes.Equal(v1, v2) || ms.salts[1].Interval != "day" {
		t.Fatalf("after the boundary: interval %q, want day with a new salt", ms.salts[1].Interval)
	}
	if v := visitor(t, m, &changed, at(t, "UTC", "2026-03-17 10:00")); bytes.Equal(v, v2) {
		t.Fatal("daily salt did not rotate the next day")
	}
}

func TestBackdatedEventBeforeSaltIsRefused(t *testing.T) {
	site := testSite(t, "day", "UTC")
	m := NewManager(&memSalts{salts: map[int64]*store.Salt{}})
	now := at(t, "UTC", "2026-05-02 12:00")
	if _, err := m.Identify(site, addr, ua, now.Add(-2*time.Hour).Unix(), now); err != nil {
		t.Fatalf("event earlier the same day: %v", err)
	}
	_, err := m.Identify(site, addr, ua, now.Add(-13*time.Hour).Unix(), now)
	if !errors.Is(err, ErrBeforeSalt) {
		t.Fatalf("event from the previous day: got %v, want ErrBeforeSalt", err)
	}
}

func TestVisits(t *testing.T) {
	site := testSite(t, "day", "UTC")
	m := NewManager(&memSalts{salts: map[int64]*store.Salt{}})
	t0 := at(t, "UTC", "2026-05-01 09:00")
	id := func(d time.Duration) Result {
		now := t0.Add(d)
		r, err := m.Identify(site, addr, ua, now.Unix(), now)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a := id(0)
	b := id(29 * time.Minute)
	c := id(29*time.Minute + 31*time.Minute)
	if !a.NewVisit || b.NewVisit || !bytes.Equal(a.Visit, b.Visit) {
		t.Fatal("events 29 minutes apart should share a visit")
	}
	if !c.NewVisit || bytes.Equal(b.Visit, c.Visit) {
		t.Fatal("an event after 31 idle minutes should start a new visit")
	}
	if !bytes.Equal(a.Visitor, c.Visitor) {
		t.Fatal("visitor should be stable across visits on one day")
	}

	other, _ := m.Identify(site, netip.MustParseAddr("192.0.2.11"), ua, t0.Unix(), t0)
	if bytes.Equal(other.Visitor, a.Visitor) {
		t.Fatal("different addresses should be different visitors")
	}
}

func TestMaskedModeIgnoresHostBitsAndUserAgent(t *testing.T) {
	site := testSite(t, "day", "UTC")
	site.Settings.IPMode = "masked"
	m := NewManager(&memSalts{salts: map[int64]*store.Salt{}})
	now := at(t, "UTC", "2026-05-01 09:00")
	a, _ := m.Identify(site, netip.MustParseAddr("192.0.2.10"), "agent one", now.Unix(), now)
	b, _ := m.Identify(site, netip.MustParseAddr("192.0.2.200"), "agent two", now.Unix(), now)
	c, _ := m.Identify(site, netip.MustParseAddr("198.51.100.10"), "agent one", now.Unix(), now)
	if !bytes.Equal(a.Visitor, b.Visitor) {
		t.Fatal("addresses in one /24 should hash alike in masked mode")
	}
	if bytes.Equal(a.Visitor, c.Visitor) {
		t.Fatal("addresses in different networks should differ")
	}
	if got := MaskAddr(netip.MustParseAddr("2001:db8:1234:5678::1")).String(); got != "2001:db8:1234::" {
		t.Fatalf("IPv6 mask: got %s", got)
	}
}

func TestSnapshotRestore(t *testing.T) {
	site := testSite(t, "day", "UTC")
	ms := &memSalts{salts: map[int64]*store.Salt{}}
	m := NewManager(ms)
	now := at(t, "UTC", "2026-05-01 09:00")
	a, _ := m.Identify(site, addr, ua, now.Unix(), now)
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snap, []byte(addr.String())) || bytes.Contains(snap, []byte(ua)) {
		t.Fatal("snapshot contains a raw address or user agent")
	}

	m2 := NewManager(ms)
	if err := m2.Restore(snap, now.Unix()); err != nil {
		t.Fatal(err)
	}
	later := now.Add(10 * time.Minute)
	b, _ := m2.Identify(site, addr, ua, later.Unix(), later)
	if b.NewVisit || !bytes.Equal(a.Visit, b.Visit) {
		t.Fatal("visit should continue after a restart")
	}

	m3 := NewManager(ms)
	m3.Restore(snap, now.Add(time.Hour).Unix())
	if m3.OpenVisits() != 0 {
		t.Fatal("timed-out visits should not be restored")
	}
}

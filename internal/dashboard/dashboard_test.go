package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jason-chao/goodwill/internal/auth"
	"github.com/jason-chao/goodwill/internal/store"
)

var now = time.Date(2026, 6, 15, 13, 30, 0, 0, time.UTC)

type client struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
}

func (c *client) do(method, path string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://example.com")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "goodwill_session" {
			c.cookie = ck
		}
	}
	return w
}

func setup(t *testing.T) (*client, *store.Store, *store.Site) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.StartWriter()
	t.Cleanup(func() { st.Close() })
	site := &store.Site{Name: "Demo <site>", Timezone: "Europe/London", SaltInterval: "day",
		Domains: []string{"example.com"}, Settings: store.DefaultSettings()}
	if err := st.CreateSite(site); err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPassword("correct-horse-battery")
	st.UpsertUser("admin", hash)

	ts := now.Add(-10 * time.Minute).Unix()
	ev := func(kind int, visitor, visit, path string, mut func(*store.Event)) {
		e := &store.Event{SiteID: site.ID, TS: ts, LocalDay: 20260615, LocalHour: 14, Kind: kind,
			Visitor: []byte(visitor + "0000000")[:8], Visit: []byte(visit + "0000000")[:8], Path: path,
			Country: "GB", Browser: "Chrome", OS: "Windows", Device: "desktop", Screen: "xl", Lang: "en-GB"}
		if mut != nil {
			mut(e)
		}
		st.Enqueue(e)
		ts += 30
	}
	ev(store.KindPageview, "A", "1", "/", func(e *store.Event) { e.RefHost, e.Channel = "google.com", "search" })
	ev(store.KindPageview, "A", "1", "/pricing", nil)
	ev(store.KindCustom, "A", "1", "/pricing", func(e *store.Event) {
		e.Name = "signup"
		e.Props = map[string]any{"plan": "<b>pro</b>", "seats": 3.0}
	})
	ev(store.KindPageview, "B", "2", "/<script>alert(1)</script>", func(e *store.Event) { e.Channel, e.Country = "direct", "" })
	st.Flush()

	srv, err := New(st, &auth.Sessions{Store: st}, "test", "geo note", "https://stats.example.com")
	if err != nil {
		t.Fatal(err)
	}
	srv.Now = func() time.Time { return now }
	return &client{t: t, h: srv.Routes()}, st, site
}

func (c *client) login() {
	c.t.Helper()
	w := c.do("POST", "/login", url.Values{"username": {"admin"}, "password": {"correct-horse-battery"}}, nil)
	if w.Code != http.StatusSeeOther || c.cookie == nil {
		c.t.Fatalf("login: status %d, cookie %v", w.Code, c.cookie)
	}
}

func TestSignInIsRequired(t *testing.T) {
	c, _, _ := setup(t)
	for _, path := range []string{"/", "/sites/1", "/sites/1/panel?dim=path", "/sites/1/event?name=signup",
		"/sites/1/realtime", "/sites/1/realtime/data", "/sites/1/setup"} {
		w := c.do("GET", path, nil, nil)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
			t.Errorf("%s without a session: status %d, location %q", path, w.Code, w.Header().Get("Location"))
		}
		if strings.Contains(w.Body.String(), "Demo") {
			t.Errorf("%s leaked content without a session", path)
		}
	}
	w := c.do("GET", "/sites/1/realtime/data", nil, map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Redirect") != "/login" {
		t.Errorf("partial without a session: status %d", w.Code)
	}
	if w := c.do("GET", "/static/app.css", nil, nil); w.Code != http.StatusOK {
		t.Errorf("static file: status %d", w.Code)
	}
}

func TestLogin(t *testing.T) {
	c, _, _ := setup(t)
	if w := c.do("GET", "/login", nil, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `name="password"`) {
		t.Fatalf("login form: status %d", w.Code)
	}
	w := c.do("POST", "/login", url.Values{"username": {"admin"}, "password": {"wrong"}}, nil)
	if w.Code != http.StatusUnauthorized || c.cookie != nil || !strings.Contains(w.Body.String(), "Wrong username or password.") {
		t.Errorf("wrong password: status %d", w.Code)
	}
	w = c.do("POST", "/login", url.Values{"username": {"nobody"}, "password": {"correct-horse-battery"}}, nil)
	if w.Code != http.StatusUnauthorized || c.cookie != nil {
		t.Errorf("unknown user: status %d", w.Code)
	}
	// A form posted from another site is refused before the password is looked at.
	w = c.do("POST", "/login", url.Values{"username": {"admin"}, "password": {"correct-horse-battery"}},
		map[string]string{"Origin": "https://evil.example"})
	if w.Code != http.StatusForbidden || c.cookie != nil {
		t.Errorf("cross-site login: status %d", w.Code)
	}

	c.login()
	if !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie flags: %+v", c.cookie)
	}
	if w := c.do("GET", "/", nil, nil); w.Code != 200 {
		t.Fatalf("after login: status %d", w.Code)
	}

	// Signing out needs the session's token.
	if w := c.do("POST", "/logout", url.Values{"csrf": {"guess"}}, nil); w.Code != http.StatusForbidden {
		t.Errorf("logout with a wrong token: status %d", w.Code)
	}
	body := c.do("GET", "/", nil, nil).Body.String()
	m := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no CSRF token in the page")
	}
	old := c.cookie
	if w := c.do("POST", "/logout", url.Values{"csrf": {m[1]}}, nil); w.Code != http.StatusSeeOther {
		t.Errorf("logout: status %d", w.Code)
	}
	c.cookie = old
	if w := c.do("GET", "/", nil, nil); w.Code != http.StatusSeeOther {
		t.Errorf("old cookie still works after logout: status %d", w.Code)
	}
}

func TestLoginThrottle(t *testing.T) {
	c, _, _ := setup(t)
	var last int
	for i := 0; i < 12; i++ {
		last = c.do("POST", "/login", url.Values{"username": {"admin"}, "password": {"wrong"}}, nil).Code
	}
	if last != http.StatusUnauthorized {
		t.Fatalf("status %d", last)
	}
	w := c.do("POST", "/login", url.Values{"username": {"admin"}, "password": {"correct-horse-battery"}}, nil)
	if c.cookie != nil || !strings.Contains(w.Body.String(), "Too many failed sign-in attempts") {
		t.Error("sign-in was not paused after repeated failures")
	}
}

func TestPages(t *testing.T) {
	c, _, _ := setup(t)
	c.login()

	body := func(path string) string {
		t.Helper()
		w := c.do("GET", path, nil, nil)
		if w.Code != 200 {
			t.Fatalf("%s: status %d: %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	has := func(path, page string, wants ...string) {
		t.Helper()
		for _, s := range wants {
			if !strings.Contains(page, s) {
				t.Errorf("%s: missing %q", path, s)
			}
		}
	}

	page := body("/")
	has("/", page, "Demo &lt;site&gt;", "Visitors today", "geo note", "<svg")

	page = body("/sites/1?period=today")
	has("site", page, "Demo &lt;site&gt;", `"visitors":[`, `"timezone":"Europe/London"`, "Bounce rate",
		"/pricing", "google.com", `data-country="GB"`, "signup", "Unknown", "Show as a table",
		`href="/sites/1?f=path%3A%2Fpricing&amp;period=today"`)
	// Values that came from visitors are escaped wherever they appear.
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Error("a page path was rendered without escaping")
	}
	has("site", page, "/&lt;script&gt;alert(1)&lt;/script&gt;")
	// Two visitors, two visits, three page views; one visit is a single page.
	for _, want := range []string{`<div class="tile-value">2</div>`, `<div class="tile-value">3</div>`, `<div class="tile-value">50%</div>`} {
		has("site tiles", page, want)
	}

	page = body("/sites/1?period=30d")
	has("30 days", page, "Visitors are recognised for one day at a time")
	if strings.Contains(body("/sites/1?period=today"), "Visitors are recognised") {
		t.Error("the salt note should not appear when the period fits inside one interval")
	}

	page = body("/sites/1?period=today&f=referrer:google.com&f=bogus:1")
	has("filtered", page, "Referrer: google.com", `<div class="tile-value">1</div>`)
	if strings.Contains(page, "bogus") {
		t.Error("an unknown filter dimension was accepted")
	}

	page = body("/sites/1?period=custom&from=2026-06-01&to=2026-06-15")
	has("custom", page, "1 Jun 2026 to 15 Jun 2026", `value="2026-06-01"`)
	has("bad custom range falls back", body("/sites/1?period=custom&from=2026-06-15&to=2026-06-01"), "Last 7 days")

	page = body("/sites/1/panel?dim=channel&period=today")
	has("panel", page, "Search", "Direct", `aria-pressed="true"`)
	if strings.Contains(page, "<html") {
		t.Error("a panel should be a fragment, not a whole page")
	}
	has("entry panel", body("/sites/1/panel?dim=entry&period=today"), "Entry page", "Visits")

	page = body("/sites/1/event?name=signup&period=today")
	has("props", page, "Properties of “signup”", "seats", "Total <strong>3</strong>", "&lt;b&gt;pro&lt;/b&gt;")
	if strings.Contains(page, "<b>pro</b>") {
		t.Error("a property value was rendered without escaping")
	}

	page = body("/sites/1/realtime")
	has("realtime", page, "Visitors in the last 5 minutes", "Latest activity", "signup", `hx-trigger="every 5s"`)
	has("realtime data", body("/sites/1/realtime/data"), "Page views and events per minute")

	page = body("/sites/1/setup")
	has("setup", page, "https://stats.example.com/script.js", "data-website-id=", "One day at a time", "example.com")

	for _, path := range []string{"/sites/999", "/sites/abc", "/nope"} {
		if w := c.do("GET", path, nil, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, w.Code)
		}
	}
	if w := c.do("GET", "/sites/1/panel?dim=visitor", nil, nil); w.Code != http.StatusBadRequest {
		t.Errorf("unknown dimension: status %d", w.Code)
	}

	w := c.do("GET", "/sites/1", nil, nil)
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("dashboard pages should not be cached")
	}
}

func TestPeriods(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	cases := []struct {
		query      string
		start, end string
		days       int
		hourly     bool
	}{
		// 13:30 UTC is 14:30 in London, still 15 June.
		{"period=today", "2026-06-15", "2026-06-16", 1, true},
		{"period=yesterday", "2026-06-14", "2026-06-15", 1, true},
		{"", "2026-06-09", "2026-06-16", 7, false},
		{"period=30d", "2026-05-17", "2026-06-16", 30, false},
		{"period=12m", "2025-06-16", "2026-06-16", 365, false},
		{"period=custom&from=2026-03-28&to=2026-03-29", "2026-03-28", "2026-03-30", 2, true},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.query)
		p := parsePeriod(q, loc, now)
		if got := p.Start.Format(dateLayout); got != c.start {
			t.Errorf("%q: start %s, want %s", c.query, got, c.start)
		}
		if got := p.End.Format(dateLayout); got != c.end {
			t.Errorf("%q: end %s, want %s", c.query, got, c.end)
		}
		if p.days() != c.days || p.Hourly != c.hourly {
			t.Errorf("%q: days %d hourly %v, want %d %v", c.query, p.days(), p.Hourly, c.days, c.hourly)
		}
		if p.Start.Hour() != 0 || p.Start.Location() != loc {
			t.Errorf("%q: start %v is not local midnight", c.query, p.Start)
		}
		prev := p.previous()
		if !prev.End.Equal(p.Start) || prev.days() != p.days() {
			t.Errorf("%q: previous period %v to %v", c.query, prev.Start, prev.End)
		}
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4200: "-4,200"} {
		if got := formatInt(in); got != want {
			t.Errorf("formatInt(%d) = %q", in, got)
		}
	}
	for in, want := range map[float64]string{3: "3", 29.5: "29.5", 1234.25: "1,234.25", 2.0 / 3: "0.67"} {
		if got := formatFloat(in); got != want {
			t.Errorf("formatFloat(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{0: "0s", 59.4: "59s", 61: "1m 01s", 3725: "1h 02m"} {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", in, got, want)
		}
	}
	if d, dir, good := delta(150, 100, true); d != "50%" || dir != "up" || !good {
		t.Errorf("delta up: %s %s %v", d, dir, good)
	}
	if d, dir, good := delta(0.6, 0.4, false); d != "50%" || dir != "up" || good {
		t.Errorf("a rising bounce rate should be marked as bad: %s %s %v", d, dir, good)
	}
	if d, _, _ := delta(5, 0, true); d != "" {
		t.Errorf("no earlier data should give no delta, got %q", d)
	}
}

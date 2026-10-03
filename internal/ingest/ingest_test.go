package ingest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jason-chao/goodwill/internal/config"
	"github.com/jason-chao/goodwill/internal/identity"
	"github.com/jason-chao/goodwill/internal/store"
)

// Addresses come from the ranges reserved for documentation.
const (
	browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	clientIP  = "198.51.100.23"
)

type env struct {
	t      *testing.T
	h      http.Handler
	in     *Handler
	st     *store.Store
	site   *store.Site
	dbPath string
	now    time.Time
}

func newEnv(t *testing.T, mutate func(*store.Site)) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.StartWriter()
	t.Cleanup(func() { st.Close() })

	site := &store.Site{Name: "Test", Timezone: "Europe/London", SaltInterval: "day", Settings: store.DefaultSettings()}
	if mutate != nil {
		mutate(site)
	}
	if err := st.CreateSite(site); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	in, err := New(cfg, st, identity.NewManager(st), nil, []byte("/* tracker */"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, h: in.Routes(), in: in, st: st, site: site, dbPath: path,
		now: time.Date(2026, 6, 15, 13, 30, 0, 0, time.UTC)}
	in.Now = func() time.Time { return e.now }
	return e
}

type reqOpt func(*http.Request)

func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func from(ip string) reqOpt     { return func(r *http.Request) { r.RemoteAddr = ip + ":40000" } }

func (e *env) post(path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case string:
		raw = []byte(b)
	default:
		raw, _ = json.Marshal(b)
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.RemoteAddr = clientIP + ":40000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", browserUA)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

// send posts one event shaped the way the tracker script sends it.
func (e *env) send(typ string, payload map[string]any, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	if _, ok := payload["website"]; !ok {
		payload["website"] = e.site.PublicID
	}
	return e.post("/api/send", map[string]any{"type": typ, "payload": payload}, opts...)
}

type row struct {
	Kind                                         int
	Name, Host, Path, Title, RefHost, Channel    sql.NullString
	UTMSource, Browser, OS, Device, Screen, Lang sql.NullString
	Country, Tag, DistinctID, Props              sql.NullString
	LCP                                          sql.NullFloat64
	TS                                           int64
	LocalDay, LocalHour                          int
	Visitor, Visit                               []byte
}

func (e *env) rows() []row {
	e.t.Helper()
	e.st.Flush()
	db, err := sql.Open("sqlite", "file:"+e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	rs, err := db.Query(`select kind, name, host, path, title, ref_host, channel, utm_source, browser, os, device,
		screen, lang, country, tag, distinct_id, props, lcp, ts, local_day, local_hour, visitor, visit from events order by id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.Kind, &r.Name, &r.Host, &r.Path, &r.Title, &r.RefHost, &r.Channel, &r.UTMSource,
			&r.Browser, &r.OS, &r.Device, &r.Screen, &r.Lang, &r.Country, &r.Tag, &r.DistinctID, &r.Props,
			&r.LCP, &r.TS, &r.LocalDay, &r.LocalHour, &r.Visitor, &r.Visit); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d; body %s", w.Code, status, w.Body.String())
	}
}

func TestPreflight(t *testing.T) {
	e := newEnv(t, nil)
	r := httptest.NewRequest("OPTIONS", "/api/send", nil)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "content-type,x-umami-cache,x-umami-hostname,x-umami-website-id")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	wantStatus(t, w, http.StatusNoContent)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q", got)
	}
	allowed := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers"))
	for _, h := range []string{"content-type", "x-umami-cache", "x-umami-website-id", "x-umami-hostname", "authorization"} {
		if !strings.Contains(allowed, h) {
			t.Errorf("Allow-Headers %q is missing %s", allowed, h)
		}
	}
}

func TestPageview(t *testing.T) {
	e := newEnv(t, nil)
	w := e.send("event", map[string]any{
		"hostname": "www.example.com",
		"language": "en-GB",
		"referrer": "https://www.google.com/search?q=secret+query",
		"screen":   "1920x1080",
		"title":    "Pricing",
		"url":      "/pricing?utm_source=newsletter&utm_medium=email&session=abc123",
	}, header("Origin", "https://www.example.com"), header("x-umami-hostname", "www.example.com"))
	wantStatus(t, w, http.StatusOK)
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("response lacks the CORS header the tracker needs to read it")
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(body) != 1 || body["ok"] != true {
		t.Errorf("response %v: want only {ok:true}, with no visitor identifiers", body)
	}

	rows := e.rows()
	if len(rows) != 1 {
		t.Fatalf("stored %d events, want 1", len(rows))
	}
	r := rows[0]
	check := func(name string, got sql.NullString, want string) {
		t.Helper()
		if got.String != want {
			t.Errorf("%s = %q, want %q", name, got.String, want)
		}
	}
	if r.Kind != store.KindPageview {
		t.Errorf("kind = %d", r.Kind)
	}
	check("host", r.Host, "example.com")
	check("path", r.Path, "/pricing")
	check("title", r.Title, "Pricing")
	check("ref_host", r.RefHost, "google.com")
	check("channel", r.Channel, "email")
	check("utm_source", r.UTMSource, "newsletter")
	check("browser", r.Browser, "Chrome")
	check("os", r.OS, "Windows")
	check("device", r.Device, "desktop")
	check("screen", r.Screen, "2xl")
	check("lang", r.Lang, "en-GB")
	// 13:30 UTC on 15 June is 14:30 in London.
	if r.LocalDay != 20260615 || r.LocalHour != 14 {
		t.Errorf("local day/hour = %d/%d, want 20260615/14", r.LocalDay, r.LocalHour)
	}
	if len(r.Visitor) != 8 || len(r.Visit) != 8 {
		t.Errorf("visitor/visit lengths %d/%d, want 8/8", len(r.Visitor), len(r.Visit))
	}
}

func TestCustomEventWithProperties(t *testing.T) {
	e := newEnv(t, nil)
	wantStatus(t, e.send("event", map[string]any{
		"hostname": "example.com", "url": "/pricing", "name": "signup", "tag": "variant-b",
		"data": map[string]any{
			"plan": "pro", "seats": 3, "trial": true, "revenue": 29.5, "currency": "USD",
			"billing": map[string]any{"cycle": "monthly"},
		},
	}), http.StatusOK)
	rows := e.rows()
	if len(rows) != 1 || rows[0].Kind != store.KindCustom || rows[0].Name.String != "signup" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Tag.String != "variant-b" {
		t.Errorf("tag = %q", rows[0].Tag.String)
	}
	var props map[string]any
	json.Unmarshal([]byte(rows[0].Props.String), &props)
	want := map[string]any{"plan": "pro", "seats": 3.0, "trial": true, "revenue": 29.5, "currency": "USD", "billing.cycle": "monthly"}
	if len(props) != len(want) {
		t.Fatalf("props = %v", props)
	}
	for k, v := range want {
		if props[k] != v {
			t.Errorf("props[%s] = %#v, want %#v", k, props[k], v)
		}
	}
}

func TestIdentifyIsOffByDefault(t *testing.T) {
	e := newEnv(t, nil)
	wantStatus(t, e.send("identify", map[string]any{
		"hostname": "example.com", "id": "user-123", "data": map[string]any{"plan": "pro"},
	}), http.StatusOK)
	wantStatus(t, e.send("event", map[string]any{"hostname": "example.com", "url": "/", "id": "user-123"}), http.StatusOK)
	for _, r := range e.rows() {
		if r.DistinctID.Valid {
			t.Errorf("distinct_id %q stored although identify is off", r.DistinctID.String)
		}
	}

	on := newEnv(t, func(s *store.Site) { s.Settings.Collect.Identify = true })
	wantStatus(t, on.send("identify", map[string]any{"hostname": "example.com", "id": "user-123"}), http.StatusOK)
	wantStatus(t, on.send("event", map[string]any{"hostname": "example.com", "url": "/"}), http.StatusOK)
	wantStatus(t, on.send("event", map[string]any{"hostname": "example.com", "url": "/account", "id": "user-123"}), http.StatusOK)
	rows := on.rows()
	if len(rows) != 3 || rows[0].Kind != store.KindIdentify || rows[0].DistinctID.String != "user-123" {
		t.Fatalf("with identify on: rows = %+v", rows)
	}
	if rows[2].DistinctID.String != "user-123" || rows[1].DistinctID.Valid {
		t.Errorf("distinct IDs %q, %q: want none, then user-123", rows[1].DistinctID.String, rows[2].DistinctID.String)
	}
	// Signing in must not turn one visitor into two.
	if !bytes.Equal(rows[1].Visitor, rows[2].Visitor) || !bytes.Equal(rows[1].Visit, rows[2].Visit) {
		t.Error("identifying changed the visitor hash or started a new visit")
	}
}

func TestPerformance(t *testing.T) {
	e := newEnv(t, nil)
	wantStatus(t, e.send("performance", map[string]any{
		"hostname": "example.com", "url": "/docs", "lcp": 1234.5, "cls": 0.02, "inp": 999999,
	}), http.StatusOK)
	rows := e.rows()
	if len(rows) != 1 || rows[0].Kind != store.KindPerformance || rows[0].LCP.Float64 != 1234.5 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestCollectionSwitches(t *testing.T) {
	e := newEnv(t, func(s *store.Site) {
		s.Settings.Collect = store.Collect{Location: "none"}
	})
	wantStatus(t, e.send("event", map[string]any{
		"hostname": "example.com", "url": "/a?utm_source=x", "referrer": "https://example.org/", "screen": "800x600",
		"language": "fr", "name": "click", "data": map[string]any{"k": "v"},
	}), http.StatusOK)
	wantStatus(t, e.send("performance", map[string]any{"hostname": "example.com", "url": "/a", "lcp": 100}), http.StatusOK)
	rows := e.rows()
	if len(rows) != 1 {
		t.Fatalf("stored %d events, want 1 (performance is switched off)", len(rows))
	}
	r := rows[0]
	for name, v := range map[string]sql.NullString{"ref_host": r.RefHost, "utm_source": r.UTMSource, "browser": r.Browser,
		"os": r.OS, "screen": r.Screen, "lang": r.Lang, "country": r.Country, "props": r.Props} {
		if v.Valid {
			t.Errorf("%s = %q stored although switched off", name, v.String)
		}
	}
}

func TestOverridesNeedTrust(t *testing.T) {
	e := newEnv(t, nil)
	key, err := e.st.NewIngestKey(e.site.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload := func() map[string]any {
		return map[string]any{
			"hostname": "example.com", "url": "/checkout", "name": "purchase",
			"ip": "203.0.113.77", "userAgent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
			"timestamp": e.now.Add(-2 * time.Hour).Unix(),
		}
	}

	// Without the key the overrides are ignored: the event is attributed to
	// the sender, at the server's own time.
	wantStatus(t, e.send("event", payload()), http.StatusOK)
	// With the key they are honoured.
	wantStatus(t, e.send("event", payload(), header("Authorization", "Bearer "+key)), http.StatusOK)
	// A wrong key is the same as none.
	wantStatus(t, e.send("event", payload(), header("Authorization", "Bearer gw_wrong")), http.StatusOK)

	rows := e.rows()
	if len(rows) != 3 {
		t.Fatalf("stored %d events, want 3", len(rows))
	}
	plain, keyed, wrong := rows[0], rows[1], rows[2]
	if plain.TS != e.now.Unix() || plain.Browser.String != "Chrome" {
		t.Errorf("untrusted sender: ts %d browser %q; overrides should be ignored", plain.TS, plain.Browser.String)
	}
	if keyed.TS != e.now.Add(-2*time.Hour).Unix() || keyed.Browser.String != "Safari" {
		t.Errorf("trusted sender: ts %d browser %q; overrides should apply", keyed.TS, keyed.Browser.String)
	}
	if bytes.Equal(plain.Visitor, keyed.Visitor) {
		t.Error("the supplied address should make a different visitor")
	}
	if !bytes.Equal(plain.Visitor, wrong.Visitor) {
		t.Error("a wrong key should be treated as an untrusted sender")
	}

	// A timestamp from before the current salt cannot be hashed any more.
	old := payload()
	old["timestamp"] = e.now.Add(-48 * time.Hour).Unix()
	wantStatus(t, e.send("event", old, header("Authorization", "Bearer "+key)), http.StatusBadRequest)
	future := payload()
	future["timestamp"] = e.now.Add(time.Hour).Unix()
	wantStatus(t, e.send("event", future, header("Authorization", "Bearer "+key)), http.StatusBadRequest)
}

func TestTrustedSourceAddress(t *testing.T) {
	e := newEnv(t, func(s *store.Site) { s.Settings.TrustedSources = []string{"192.0.2.0/28"} })
	p := map[string]any{"hostname": "example.com", "url": "/", "name": "job", "ip": "203.0.113.5"}
	// The stock server-side client sends this kind of user agent and no key.
	wantStatus(t, e.send("event", p, from("192.0.2.5"), header("User-Agent", "Mozilla/5.0 Umami/v22.0.0")), http.StatusOK)
	if rows := e.rows(); len(rows) != 1 {
		t.Fatalf("stored %d events from a trusted source, want 1", len(rows))
	}
}

func TestBatchNeedsKey(t *testing.T) {
	e := newEnv(t, nil)
	key, _ := e.st.NewIngestKey(e.site.ID)
	batch := []map[string]any{
		{"type": "event", "payload": map[string]any{"website": e.site.PublicID, "hostname": "example.com", "url": "/a"}},
		{"type": "event", "payload": map[string]any{"website": e.site.PublicID, "hostname": "example.com", "url": "/b", "name": "x"}},
		{"type": "event", "payload": map[string]any{"website": "00000000-0000-4000-8000-000000000000", "url": "/c"}},
	}
	var res struct{ Size, Processed, Errors int }

	w := e.post("/api/batch", batch)
	wantStatus(t, w, http.StatusOK)
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Processed != 0 || res.Errors != 3 {
		t.Errorf("without a key: %+v, want nothing processed", res)
	}

	w = e.post("/api/batch", batch, header("Authorization", "Bearer "+key))
	wantStatus(t, w, http.StatusOK)
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Size != 3 || res.Processed != 2 || res.Errors != 1 {
		t.Errorf("with a key: %+v, want 2 of 3 processed", res)
	}
	if rows := e.rows(); len(rows) != 2 {
		t.Errorf("stored %d events, want 2", len(rows))
	}
}

func TestAllowedDomains(t *testing.T) {
	e := newEnv(t, func(s *store.Site) { s.Domains = []string{"example.com", "*.example.org"} })
	ok := func(host, origin string) int {
		opts := []reqOpt{}
		if origin != "" {
			opts = append(opts, header("Origin", origin))
		}
		return e.send("event", map[string]any{"hostname": host, "url": "/"}, opts...).Code
	}
	cases := []struct {
		host, origin string
		want         int
	}{
		{"example.com", "https://example.com", 200},
		{"www.example.com", "https://www.example.com", 200},
		{"app.example.org", "https://app.example.org", 200},
		{"example.org", "https://example.org", 200},
		{"evil.example", "https://evil.example", 403},
		// The hostname in the body is chosen by the sender; the browser's
		// Origin header is not.
		{"example.com", "https://evil.example", 403},
		{"sub.example.com", "", 403},
		{"localhost", "http://localhost:3000", 403},
		{"", "", 403},
	}
	for _, c := range cases {
		if got := ok(c.host, c.origin); got != c.want {
			t.Errorf("hostname %q origin %q: status %d, want %d", c.host, c.origin, got, c.want)
		}
	}
}

func TestSilentlyDropped(t *testing.T) {
	e := newEnv(t, func(s *store.Site) { s.Settings.IgnoreIPs = []string{"198.51.100.99", "2001:db8:aaaa::/48"} })
	p := func() map[string]any { return map[string]any{"hostname": "example.com", "url": "/"} }
	for name, opt := range map[string]reqOpt{
		"crawler":      header("User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"),
		"script":       header("User-Agent", "curl/8.5.0"),
		"no agent":     header("User-Agent", ""),
		"ignored IPv4": from("198.51.100.99"),
	} {
		wantStatus(t, e.send("event", p(), opt), http.StatusOK)
		if n := len(e.rows()); n != 0 {
			t.Errorf("%s: stored %d events, want 0", name, n)
		}
	}
}

func TestRejected(t *testing.T) {
	e := newEnv(t, nil)
	cases := []struct {
		name string
		body any
		want int
	}{
		{"not JSON", "{", 400},
		{"unknown website", map[string]any{"type": "event", "payload": map[string]any{"website": "00000000-0000-4000-8000-000000000000", "url": "/"}}, 400},
		{"no website", map[string]any{"type": "event", "payload": map[string]any{"url": "/"}}, 400},
		{"unknown type", map[string]any{"type": "delete", "payload": map[string]any{"website": e.site.PublicID}}, 400},
		{"formula in name", map[string]any{"type": "event", "payload": map[string]any{"website": e.site.PublicID, "url": "/", "name": "=HYPERLINK(1)"}}, 400},
		{"data is not an object", map[string]any{"type": "event", "payload": map[string]any{"website": e.site.PublicID, "name": "x", "data": "text"}}, 400},
		{"oversized", `{"type":"event","payload":{"title":"` + strings.Repeat("x", 70<<10) + `"}}`, 400},
	}
	for _, c := range cases {
		if w := e.post("/api/send", c.body); w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
	if n := len(e.rows()); n != 0 {
		t.Errorf("stored %d events from rejected requests", n)
	}
}

func TestWebsiteIDFromHeader(t *testing.T) {
	e := newEnv(t, nil)
	w := e.post("/api/send", map[string]any{"type": "event", "payload": map[string]any{"hostname": "example.com", "url": "/"}},
		header("x-umami-website-id", e.site.PublicID))
	wantStatus(t, w, http.StatusOK)
	if n := len(e.rows()); n != 1 {
		t.Errorf("stored %d events, want 1", n)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, nil)
	e.in.limit.perMinute = 5
	var codes []int
	for i := 0; i < 7; i++ {
		codes = append(codes, e.send("event", map[string]any{"hostname": "example.com", "url": "/"}).Code)
	}
	if codes[4] != 200 || codes[5] != 429 || codes[6] != 429 {
		t.Errorf("statuses %v: want five accepted, then 429", codes)
	}
	if w := e.send("event", map[string]any{"hostname": "example.com", "url": "/"}, from("198.51.100.24")); w.Code != 200 {
		t.Errorf("another address was limited too: %d", w.Code)
	}
	e.now = e.now.Add(time.Minute)
	if w := e.send("event", map[string]any{"hostname": "example.com", "url": "/"}); w.Code != 200 {
		t.Errorf("still limited in the next minute: %d", w.Code)
	}
}

func TestClientIPBehindProxy(t *testing.T) {
	e := newEnv(t, nil)
	e.in.proxies, _ = config.ParsePrefixes([]string{"127.0.0.1", "10.0.0.0/8"})
	get := func(remote, xff string) string {
		r := httptest.NewRequest("POST", "/api/send", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return e.in.clientIP(r).String()
	}
	cases := []struct{ remote, xff, want string }{
		{"127.0.0.1:5000", "198.51.100.7", "198.51.100.7"},
		// Anything the client put in the header itself sits to the left of
		// what the proxy appended, and is not believed.
		{"127.0.0.1:5000", "203.0.113.1, 198.51.100.7", "198.51.100.7"},
		{"127.0.0.1:5000", "198.51.100.7, 10.1.2.3", "198.51.100.7"},
		{"127.0.0.1:5000", "", "127.0.0.1"},
		// Not from a trusted proxy: the header is ignored entirely.
		{"198.51.100.50:5000", "203.0.113.1", "198.51.100.50"},
	}
	for _, c := range cases {
		if got := get(c.remote, c.xff); got != c.want {
			t.Errorf("remote %s, forwarded %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestVisitsAndChannel(t *testing.T) {
	e := newEnv(t, nil)
	view := func(url, ref string) {
		t.Helper()
		wantStatus(t, e.send("event", map[string]any{"hostname": "example.com", "url": url, "referrer": ref}), http.StatusOK)
	}
	view("/", "https://news.ycombinator.com/item?id=1")
	e.now = e.now.Add(5 * time.Minute)
	view("/pricing", "https://example.com/")
	e.now = e.now.Add(45 * time.Minute)
	view("/docs", "")
	rows := e.rows()
	if len(rows) != 3 {
		t.Fatalf("stored %d events", len(rows))
	}
	if !bytes.Equal(rows[0].Visit, rows[1].Visit) || bytes.Equal(rows[1].Visit, rows[2].Visit) {
		t.Error("want the first two page views in one visit and the third in another")
	}
	if rows[0].Channel.String != "social" || rows[1].Channel.Valid || rows[2].Channel.String != "direct" {
		t.Errorf("channels %q, %q, %q: want social, none, direct",
			rows[0].Channel.String, rows[1].Channel.String, rows[2].Channel.String)
	}
	if rows[1].RefHost.Valid {
		t.Errorf("a referrer on the site itself was stored: %q", rows[1].RefHost.String)
	}
}

// TestNoRawIdentifiersOnDisk sends traffic and then reads the database files
// back byte for byte: no address and no user agent string may appear in them.
func TestNoRawIdentifiersOnDisk(t *testing.T) {
	e := newEnv(t, func(s *store.Site) {
		s.Settings.Collect.Identify = true
		s.Settings.Collect.Location = "city"
	})
	key, _ := e.st.NewIngestKey(e.site.ID)
	addrs := []string{"198.51.100.23", "198.51.100.200", "203.0.113.9", "2001:db8:85a3::8a2e:370:7334"}
	agents := []string{
		browserUA,
		"Mozilla/5.0 (X11; Linux x86_64; rv:142.0) Gecko/20100101 Firefox/142.0 UniqueMarkerAlpha",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15 UniqueMarkerBeta",
	}
	for i, a := range addrs {
		ua := agents[i%len(agents)]
		wantStatus(t, e.send("event", map[string]any{"hostname": "example.com", "url": "/p", "referrer": "https://example.org/x"},
			from(a), header("User-Agent", ua)), http.StatusOK)
		wantStatus(t, e.send("event", map[string]any{"hostname": "example.com", "url": "/p", "name": "click", "data": map[string]any{"n": i}},
			from(a), header("User-Agent", ua)), http.StatusOK)
		wantStatus(t, e.send("identify", map[string]any{"hostname": "example.com", "id": "u" + a[:3], "data": map[string]any{"plan": "pro"}},
			from(a), header("User-Agent", ua)), http.StatusOK)
	}
	// A trusted sender passing a visitor's details on.
	wantStatus(t, e.send("event", map[string]any{"hostname": "example.com", "url": "/server", "name": "job",
		"ip": "203.0.113.250", "userAgent": "Mozilla/5.0 (Windows NT 10.0) UniqueMarkerGamma Chrome/140.0.0.0"},
		header("Authorization", "Bearer "+key)), http.StatusOK)
	// A request that gets rate limited still passes through the limiter.
	e.in.limit.perMinute = 1
	e.send("event", map[string]any{"hostname": "example.com", "url": "/"}, from("198.51.100.77"))
	e.send("event", map[string]any{"hostname": "example.com", "url": "/"}, from("198.51.100.77"))

	if n := len(e.rows()); n < 13 {
		t.Fatalf("only %d events stored; the test did not exercise the write path", n)
	}
	// Close the store so that everything, including the visit snapshot a
	// shutdown writes, is on disk.
	snap, err := e.in.ident.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	e.st.Flush()
	if err := e.st.SetKV("open_visits", snap); err != nil {
		t.Fatal(err)
	}

	needles := append([]string{"198.51.100.", "203.0.113.", "2001:db8", "2001:0db8", "UniqueMarker", "Mozilla/5.0", "AppleWebKit", "Gecko/"}, addrs...)
	files, _ := filepath.Glob(e.dbPath + "*")
	if len(files) == 0 {
		t.Fatal("no database files found")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range needles {
			if bytes.Contains(data, []byte(n)) {
				t.Errorf("%s contains %q", filepath.Base(f), n)
			}
		}
	}
}

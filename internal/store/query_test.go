package store

import (
	"path/filepath"
	"reflect"
	"testing"
)

// seed builds a small, hand-checkable dataset on one site:
//
//	visitor A, visit 1 (day 1): / (from google, search) -> /pricing -> signup{plan:pro, seats:3, revenue:29}
//	visitor B, visit 2 (day 1): /pricing (direct)                      bounce
//	visitor C, visit 3 (day 2): /docs (from t.co, social) -> /         -> signup{plan:free, seats:1}
//	visitor A', visit 4 (day 2): /                                      bounce (A again, new salt)
//	a performance record and an identify record, which must not count as activity
func seed(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.StartWriter()
	t.Cleanup(func() { st.Close() })
	site := &Site{Name: "Q", Timezone: "UTC", SaltInterval: "day", Settings: DefaultSettings()}
	if err := st.CreateSite(site); err != nil {
		t.Fatal(err)
	}
	const day1, day2 = 1000000, 1086400
	lcp := 1200.0
	b := func(s string) []byte { return []byte(s + "0000000")[:8] }
	ev := []Event{
		{TS: day1 + 10, LocalDay: 20260101, LocalHour: 9, Kind: KindPageview, Visitor: b("A"), Visit: b("1"), Path: "/", RefHost: "google.com", Channel: "search", Country: "GB", Browser: "Chrome"},
		{TS: day1 + 70, LocalDay: 20260101, LocalHour: 9, Kind: KindPageview, Visitor: b("A"), Visit: b("1"), Path: "/pricing", Country: "GB", Browser: "Chrome"},
		{TS: day1 + 130, LocalDay: 20260101, LocalHour: 9, Kind: KindCustom, Name: "signup", Visitor: b("A"), Visit: b("1"), Path: "/pricing", Country: "GB", Browser: "Chrome",
			Props: map[string]any{"plan": "pro", "seats": 3.0, "revenue": 29.0, "trial": true}},
		{TS: day1 + 500, LocalDay: 20260101, LocalHour: 10, Kind: KindPageview, Visitor: b("B"), Visit: b("2"), Path: "/pricing", Channel: "direct", Country: "US", Browser: "Safari"},
		{TS: day1 + 501, LocalDay: 20260101, LocalHour: 10, Kind: KindPerformance, Visitor: b("B"), Visit: b("2"), Path: "/pricing", Country: "US", Browser: "Safari", LCP: &lcp},
		{TS: day2 + 10, LocalDay: 20260102, LocalHour: 9, Kind: KindPageview, Visitor: b("C"), Visit: b("3"), Path: "/docs", RefHost: "t.co", Channel: "social", Country: "US", Browser: "Chrome"},
		{TS: day2 + 40, LocalDay: 20260102, LocalHour: 9, Kind: KindPageview, Visitor: b("C"), Visit: b("3"), Path: "/", Country: "US", Browser: "Chrome"},
		{TS: day2 + 50, LocalDay: 20260102, LocalHour: 9, Kind: KindCustom, Name: "signup", Visitor: b("C"), Visit: b("3"), Path: "/", Country: "US", Browser: "Chrome",
			Props: map[string]any{"plan": "free", "seats": 1.0, "trial": false}},
		{TS: day2 + 51, LocalDay: 20260102, LocalHour: 9, Kind: KindIdentify, Visitor: b("C"), Visit: b("3"), Country: "US", Browser: "Chrome", Props: map[string]any{"tier": "x"}},
		{TS: day2 + 900, LocalDay: 20260102, LocalHour: 9, Kind: KindPageview, Visitor: b("D"), Visit: b("4"), Path: "/", Channel: "direct", Browser: "Chrome"},
	}
	for i := range ev {
		ev[i].SiteID = site.ID
		if err := st.Enqueue(&ev[i]); err != nil {
			t.Fatal(err)
		}
	}
	// Another site's traffic must never leak into the first site's numbers.
	other := &Site{Name: "Other", Timezone: "UTC", SaltInterval: "day", Settings: DefaultSettings()}
	st.CreateSite(other)
	st.Enqueue(&Event{SiteID: other.ID, TS: day1 + 5, LocalDay: 20260101, Kind: KindPageview, Visitor: b("Z"), Visit: b("9"), Path: "/other"})
	st.Flush()
	return st, site.ID
}

func all(site int64, f ...Filter) Query {
	return Query{SiteID: site, From: 0, To: 2000000, Filters: f}
}

func TestStats(t *testing.T) {
	st, site := seed(t)
	cases := []struct {
		name string
		q    Query
		want Stats
	}{
		{"everything", all(site), Stats{Views: 6, Visitors: 4, Visits: 4, Bounces: 2, Duration: 120 + 0 + 40 + 0}},
		{"day one only", Query{SiteID: site, From: 1000000, To: 1086400}, Stats{Views: 3, Visitors: 2, Visits: 2, Bounces: 1, Duration: 120}},
		{"country US", all(site, Filter{"country", "US"}), Stats{Views: 3, Visitors: 2, Visits: 2, Bounces: 1, Duration: 40}},
		{"unknown country", all(site, Filter{"country", None}), Stats{Views: 1, Visitors: 1, Visits: 1, Bounces: 1}},
		// A visit-level filter keeps every event of the visits that match.
		{"came from google", all(site, Filter{"referrer", "google.com"}), Stats{Views: 2, Visitors: 1, Visits: 1, Duration: 120}},
		{"fired signup", all(site, Filter{"event", "signup"}), Stats{Views: 4, Visitors: 2, Visits: 2, Duration: 160}},
		{"channel direct", all(site, Filter{"channel", "direct"}), Stats{Views: 2, Visitors: 2, Visits: 2, Bounces: 2}},
		// A page filter keeps only that page's rows.
		{"page /pricing", all(site, Filter{"path", "/pricing"}), Stats{Views: 2, Visitors: 2, Visits: 2, Bounces: 1, Duration: 60}},
		{"two filters", all(site, Filter{"country", "US"}, Filter{"browser", "Chrome"}), Stats{Views: 2, Visitors: 1, Visits: 1, Duration: 40}},
		{"no match", all(site, Filter{"country", "FR"}), Stats{}},
	}
	for _, c := range cases {
		got, err := st.Stats(c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	s := Stats{Visits: 4, Bounces: 2, Duration: 160}
	if s.BounceRate() != 0.5 || s.AvgDuration() != 40 {
		t.Errorf("rates: %v %v", s.BounceRate(), s.AvgDuration())
	}
	if (Stats{}).BounceRate() != 0 || (Stats{}).AvgDuration() != 0 {
		t.Error("empty stats should give zero rates")
	}
}

func TestSeries(t *testing.T) {
	st, site := seed(t)
	daily, err := st.Series(all(site), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []Point{{20260101, -1, 3, 2}, {20260102, -1, 3, 2}}
	if !reflect.DeepEqual(daily, want) {
		t.Errorf("daily = %+v, want %+v", daily, want)
	}
	hourly, _ := st.Series(all(site), true)
	wantH := []Point{{20260101, 9, 2, 1}, {20260101, 10, 1, 1}, {20260102, 9, 3, 2}}
	if !reflect.DeepEqual(hourly, wantH) {
		t.Errorf("hourly = %+v, want %+v", hourly, wantH)
	}
}

func TestBreakdown(t *testing.T) {
	st, site := seed(t)
	cases := []struct {
		dim  string
		q    Query
		want []Row
	}{
		{"path", all(site), []Row{{"/", 3, 3}, {"/pricing", 2, 2}, {"/docs", 1, 1}}},
		{"entry", all(site), []Row{{"/", 2, 2}, {"/docs", 1, 1}, {"/pricing", 1, 1}}},
		{"referrer", all(site), []Row{{"google.com", 1, 1}, {"t.co", 1, 1}}},
		{"channel", all(site), []Row{{"direct", 2, 2}, {"search", 1, 1}, {"social", 1, 1}}},
		{"country", all(site), []Row{{"US", 2, 4}, {"GB", 1, 3}, {"", 1, 1}}},
		{"browser", all(site), []Row{{"Chrome", 3, 7}, {"Safari", 1, 1}}},
		{"event", all(site), []Row{{"signup", 2, 2}}},
		{"path", all(site, Filter{"event", "signup"}), []Row{{"/", 2, 2}, {"/docs", 1, 1}, {"/pricing", 1, 1}}},
		{"region", all(site), nil},
	}
	for _, c := range cases {
		got, err := st.Breakdown(c.q, c.dim, 10)
		if err != nil {
			t.Fatalf("%s: %v", c.dim, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %v: got %+v, want %+v", c.dim, c.q.Filters, got, c.want)
		}
	}
	if got, _ := st.Breakdown(all(site), "path", 2); len(got) != 2 {
		t.Errorf("limit 2 returned %d rows", len(got))
	}
	if _, err := st.Breakdown(all(site), "visitor; drop table events", 10); err == nil {
		t.Error("an unknown dimension should be refused")
	}
}

func TestEventProps(t *testing.T) {
	st, site := seed(t)
	props, err := st.EventProps(all(site), "signup", 10)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Prop{}
	for _, p := range props {
		byKey[p.Key] = p
	}
	if len(props) != 4 {
		t.Fatalf("got %d properties, want plan, revenue, seats, trial: %+v", len(props), props)
	}
	seats := byKey["seats"]
	if !seats.Numeric || seats.Count != 2 || seats.Sum != 4 || seats.Avg != 2 || seats.Min != 1 || seats.Max != 3 {
		t.Errorf("seats = %+v", seats)
	}
	if rev := byKey["revenue"]; !rev.Numeric || rev.Count != 1 || rev.Sum != 29 {
		t.Errorf("revenue = %+v", rev)
	}
	plan := byKey["plan"]
	if plan.Numeric || !reflect.DeepEqual(plan.Values, []PropValue{{"free", 1, 1}, {"pro", 1, 1}}) {
		t.Errorf("plan = %+v", plan)
	}
	if trial := byKey["trial"]; !reflect.DeepEqual(trial.Values, []PropValue{{"false", 1, 1}, {"true", 1, 1}}) {
		t.Errorf("trial = %+v", trial)
	}
	if got, _ := st.EventProps(all(site, Filter{"country", "GB"}), "signup", 10); len(got) != 4 || got[0].Count != 1 {
		t.Errorf("filtered by country: %+v", got)
	}
	if got, _ := st.EventProps(all(site), "missing", 10); len(got) != 0 {
		t.Errorf("unknown event: %+v", got)
	}
}

func TestRealtimeAndDaily(t *testing.T) {
	st, site := seed(t)
	rt, err := st.Realtime(site, 1086400+960)
	if err != nil {
		t.Fatal(err)
	}
	// In the 5 minutes before: only visitor D. In the 30 before: C's three
	// events and D's one; the identify record does not count.
	if rt.Active != 1 || len(rt.Recent) != 4 {
		t.Errorf("active %d, recent %d; want 1 and 4", rt.Active, len(rt.Recent))
	}
	if rt.Recent[0].Path != "/" || rt.Recent[0].TS != 1086400+900 {
		t.Errorf("newest first: got %+v", rt.Recent[0])
	}
	var total int64
	for _, n := range rt.Minutes {
		total += n
	}
	if total != 4 {
		t.Errorf("per-minute counts sum to %d, want 4", total)
	}
	daily, _ := st.DailyViews(site, 0)
	if !reflect.DeepEqual(daily, map[int]int64{20260101: 3, 20260102: 3}) {
		t.Errorf("daily views = %v", daily)
	}
}

func TestPropertyCatalogue(t *testing.T) {
	st, site := seed(t)
	rows, err := st.r.Query(`select event, key, type from property_keys where site_id = ? order by event, key`, site)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var e, k, ty string
		rows.Scan(&e, &k, &ty)
		got = append(got, e+"/"+k+"="+ty)
	}
	want := []string{"/tier=string", "signup/plan=string", "signup/revenue=number", "signup/seats=number", "signup/trial=boolean"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("catalogue = %v, want %v", got, want)
	}
}

func TestSitesAndKeys(t *testing.T) {
	st, id := seed(t)
	site, _ := st.Site(id)
	if site.CheckIngestKey("anything") || site.CheckIngestKey("") {
		t.Error("a site without a key accepted one")
	}
	key, err := st.NewIngestKey(id)
	if err != nil {
		t.Fatal(err)
	}
	site, _ = st.SiteByPublicID(site.PublicID)
	if !site.CheckIngestKey(key) || site.CheckIngestKey(key+"x") {
		t.Error("ingest key check is wrong")
	}
	for _, bad := range []*Site{
		{Name: "", Timezone: "UTC", SaltInterval: "day", Settings: DefaultSettings()},
		{Name: "x", Timezone: "Mars/Olympus", SaltInterval: "day", Settings: DefaultSettings()},
		{Name: "x", Timezone: "UTC", SaltInterval: "hour", Settings: DefaultSettings()},
		{Name: "x", Timezone: "UTC", SaltInterval: "0d", Settings: DefaultSettings()},
	} {
		if err := st.CreateSite(bad); err == nil {
			t.Errorf("site %+v should be refused", bad)
		}
	}
	for _, ok := range []string{"day", "week", "month", "1d", "3d", "90d"} {
		if !ValidSaltInterval(ok) {
			t.Errorf("%q should be a valid interval", ok)
		}
	}
	if none, _ := st.SiteByPublicID("00000000-0000-4000-8000-000000000000"); none != nil {
		t.Error("unknown public ID returned a site")
	}
}

func TestBackupAndReopen(t *testing.T) {
	st, site := seed(t)
	dest := filepath.Join(t.TempDir(), "copy.db")
	if err := st.Backup(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	copyDB, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	got, _ := copyDB.Stats(all(site))
	if got.Views != 6 {
		t.Errorf("backup has %d views, want 6", got.Views)
	}
	if v, _ := copyDB.SchemaVersion(); v != 1 {
		t.Errorf("schema version %d, want 1", v)
	}
}

func TestQueueRefusesWhenFull(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A writer that is never started stands in for one that has stalled.
	st.writer = &writer{ch: make(chan *Event, 2), done: make(chan struct{})}
	close(st.writer.done)
	e := &Event{Visitor: []byte("v"), Visit: []byte("v")}
	for i := 0; i < 2; i++ {
		if err := st.Enqueue(e); err != nil {
			t.Fatalf("queue refused event %d while it had room: %v", i+1, err)
		}
	}
	if err := st.Enqueue(e); err != ErrQueueFull {
		t.Errorf("full queue: got %v, want ErrQueueFull", err)
	}
	st.writer = nil
}

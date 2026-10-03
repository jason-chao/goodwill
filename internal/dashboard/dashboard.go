// Package dashboard serves the private web interface.
package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jason-chao/goodwill/internal/auth"
	"github.com/jason-chao/goodwill/internal/store"
	"github.com/jason-chao/goodwill/web"
)

// Server serves the dashboard.
type Server struct {
	store    *store.Store
	sessions *auth.Sessions
	tmpl     *template.Template
	static   http.Handler
	version  string
	geoNote  string
	public   string

	// Now is the clock; tests replace it.
	Now func() time.Time
}

// New builds the dashboard. geoNote is the attribution shown in the footer
// for the location database in use, if any.
//
// publicURL is the address visitors' browsers reach the public listener at;
// it is only used to fill in the tracking snippet.
func New(st *store.Store, sessions *auth.Sessions, version, geoNote, publicURL string) (*Server, error) {
	tmpl, err := template.New("").Funcs(funcs).ParseFS(web.FS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		return nil, err
	}
	return &Server{
		store: st, sessions: sessions, tmpl: tmpl, version: version, geoNote: geoNote,
		public: strings.TrimRight(publicURL, "/"),
		static: http.StripPrefix("/static/", http.FileServerFS(static)),
		Now:    time.Now,
	}, nil
}

// Routes returns the admin mux.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		s.static.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /{$}", s.user(s.sites))
	mux.HandleFunc("GET /sites/{id}", s.user(s.site))
	mux.HandleFunc("GET /sites/{id}/panel", s.user(s.panel))
	mux.HandleFunc("GET /sites/{id}/event", s.user(s.eventProps))
	mux.HandleFunc("GET /sites/{id}/realtime", s.user(s.realtime))
	mux.HandleFunc("GET /sites/{id}/realtime/data", s.user(s.realtimeData))
	mux.HandleFunc("GET /sites/{id}/setup", s.user(s.setup))
	return headers(mux)
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Robots-Tag", "noindex")
		next.ServeHTTP(w, r)
	})
}

type ctx struct {
	w    http.ResponseWriter
	r    *http.Request
	sess *store.AuthSession
}

// user wraps a handler that needs a signed-in session.
func (s *Server) user(fn func(c *ctx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.sessions.Current(r)
		if sess == nil {
			if r.Header.Get("HX-Request") != "" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		fn(&ctx{w, r, sess})
	}
}

// base is the data every full page needs.
type base struct {
	Title    string
	Username string
	CSRF     string
	Version  string
	GeoNote  string
}

func (s *Server) base(c *ctx, title string) base {
	b := base{Title: title, Version: s.version, GeoNote: s.geoNote}
	if c != nil && c.sess != nil {
		b.Username, b.CSRF = c.sess.Username, c.sess.CSRF
	}
	return b
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("rendering failed", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	slog.Error("dashboard request failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if s.sessions.Current(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "login.html", map[string]any{"Base": s.base(nil, "Sign in")})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !auth.SameOrigin(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	err := s.sessions.Login(w, username, r.PostFormValue("password"))
	if err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	status, msg := http.StatusUnauthorized, err.Error()
	if !errors.Is(err, auth.ErrBadLogin) && !errors.Is(err, auth.ErrThrottled) {
		slog.Error("sign-in failed", "err", err)
		status, msg = http.StatusInternalServerError, "Something went wrong. Check the server log."
	} else {
		msg = strings.ToUpper(msg[:1]) + msg[1:] + "."
	}
	s.render(w, status, "login.html", map[string]any{
		"Base": s.base(nil, "Sign in"), "Error": msg, "Username": username,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	sess := s.sessions.Current(r)
	if !auth.SameOrigin(r) || !auth.CheckCSRF(sess, r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	s.sessions.Logout(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// siteFor loads the site named in the URL, writing a 404 if there is none.
func (s *Server) siteFor(c *ctx) *store.Site {
	id, err := strconv.ParseInt(c.r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(c.w, c.r)
		return nil
	}
	site, err := s.store.Site(id)
	if err != nil {
		s.fail(c.w, err)
		return nil
	}
	if site == nil {
		http.NotFound(c.w, c.r)
	}
	return site
}

type siteCard struct {
	Site     *store.Site
	Visitors int64
	Views    int64
	Spark    template.HTML
	Total    int64
}

func (s *Server) sites(c *ctx) {
	sites, err := s.store.Sites()
	if err != nil {
		s.fail(c.w, err)
		return
	}
	now := s.Now()
	const sparkDays = 14
	cards := make([]siteCard, 0, len(sites))
	for _, site := range sites {
		p := parsePeriod(url.Values{"period": {"today"}}, site.Location(), now)
		st, err := s.store.Stats(p.query(site.ID, nil))
		if err != nil {
			s.fail(c.w, err)
			return
		}
		start := p.Start.AddDate(0, 0, -(sparkDays - 1))
		daily, err := s.store.DailyViews(site.ID, start.Unix())
		if err != nil {
			s.fail(c.w, err)
			return
		}
		values := make([]int64, sparkDays)
		var total int64
		for i := range values {
			values[i] = daily[dayKey(start.AddDate(0, 0, i))]
			total += values[i]
		}
		cards = append(cards, siteCard{site, st.Visitors, st.Views, sparkline(values), total})
	}
	s.render(c.w, http.StatusOK, "sites.html", map[string]any{
		"Base": s.base(c, "Sites"), "Cards": cards, "SparkDays": sparkDays,
	})
}

func dayKey(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

// tile is one headline number with its change from the previous period.
type tile struct {
	Label string
	Value string
	Delta string // e.g. "12%", empty when there is nothing to compare with
	Dir   string // "up", "down" or "flat"
	Good  bool   // whether the direction is the desirable one
}

func delta(cur, prev float64, upIsGood bool) (string, string, bool) {
	if prev == 0 {
		return "", "flat", true
	}
	change := (cur - prev) / prev * 100
	switch {
	case change >= 0.5:
		return fmt.Sprintf("%.0f%%", change), "up", upIsGood
	case change <= -0.5:
		return fmt.Sprintf("%.0f%%", -change), "down", !upIsGood
	}
	return "0%", "flat", true
}

func tiles(cur, prev store.Stats) []tile {
	mk := func(label, value string, c, p float64, upIsGood bool) tile {
		d, dir, good := delta(c, p, upIsGood)
		return tile{label, value, d, dir, good}
	}
	return []tile{
		mk("Visitors", formatInt(cur.Visitors), float64(cur.Visitors), float64(prev.Visitors), true),
		mk("Visits", formatInt(cur.Visits), float64(cur.Visits), float64(prev.Visits), true),
		mk("Page views", formatInt(cur.Views), float64(cur.Views), float64(prev.Views), true),
		mk("Bounce rate", fmt.Sprintf("%.0f%%", cur.BounceRate()*100), cur.BounceRate(), prev.BounceRate(), false),
		mk("Visit duration", formatDuration(cur.AvgDuration()), cur.AvgDuration(), prev.AvgDuration(), true),
	}
}

type chip struct {
	Dim, Label, Value, RemoveURL string
}

type periodLink struct {
	Label  string
	URL    string
	Active bool
}

func nav(site *store.Site, tab string) map[string]any {
	return map[string]any{"Site": site, "Tab": tab}
}

type chartData struct {
	X        []int64 `json:"x"`
	Visitors []int64 `json:"visitors"`
	Views    []int64 `json:"views"`
	Hourly   bool    `json:"hourly"`
	Timezone string  `json:"timezone"`
}

type tableRow struct {
	Label           string
	Visitors, Views int64
}

// series fills the gaps in the stored buckets so the chart has a point for
// every hour or day of the period.
func (s *Server) series(q store.Query, p period, loc *time.Location) (chartData, []tableRow, error) {
	points, err := s.store.Series(q, p.Hourly)
	if err != nil {
		return chartData{}, nil, err
	}
	type key struct{ day, hour int }
	have := make(map[key]store.Point, len(points))
	for _, pt := range points {
		have[key{pt.Day, pt.Hour}] = pt
	}
	cd := chartData{Hourly: p.Hourly, Timezone: loc.String()}
	var rows []tableRow
	for t := p.Start; t.Before(p.End); {
		k, label := key{dayKey(t), -1}, t.Format("Mon 2 Jan 2006")
		if p.Hourly {
			k.hour, label = t.Hour(), t.Format("Mon 2 Jan, 15:04")
		}
		pt := have[k]
		cd.X = append(cd.X, t.Unix())
		cd.Visitors = append(cd.Visitors, pt.Visitors)
		cd.Views = append(cd.Views, pt.Views)
		rows = append(rows, tableRow{label, pt.Visitors, pt.Views})
		if p.Hourly {
			t = t.Add(time.Hour)
		} else {
			t = t.AddDate(0, 0, 1)
		}
	}
	return cd, rows, nil
}

func (s *Server) site(c *ctx) {
	site := s.siteFor(c)
	if site == nil {
		return
	}
	q := c.r.URL.Query()
	p := parsePeriod(q, site.Location(), s.Now())
	filters := parseFilters(q)
	query := p.query(site.ID, filters)

	cur, err := s.store.Stats(query)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	prev, err := s.store.Stats(p.previous().query(site.ID, filters))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	cd, rows, err := s.series(query, p, site.Location())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	chartJSON, _ := json.Marshal(cd)

	var cards []cardView
	for _, def := range cardDefs {
		pv, err := s.buildPanel(site, p, filters, def.Tabs[0].Dim, panelLimit)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		cards = append(cards, cardView{Def: def, Panel: pv})
	}

	base := "/sites/" + itoa(site.ID)
	var chips []chip
	for _, f := range filters {
		chips = append(chips, chip{f.Dim, dimLabel(f.Dim), f.Value, base + "?" + queryString(p, filters, f.Dim, "")})
	}
	var periods []periodLink
	for _, o := range periodOptions {
		qs := url.Values{"period": {o.Key}}
		for _, f := range filters {
			qs.Add("f", f.Dim+":"+f.Value)
		}
		periods = append(periods, periodLink{o.Label, base + "?" + qs.Encode(), o.Key == p.Key})
	}
	prevLabel := "day"
	if n := p.days(); n > 1 {
		prevLabel = itoa(int64(n)) + " days"
	}
	s.render(c.w, http.StatusOK, "site.html", map[string]any{
		"Base":      s.base(c, site.Name),
		"Site":      site,
		"Nav":       nav(site, "overview"),
		"Period":    p,
		"Periods":   periods,
		"PrevLabel": prevLabel,
		"From":      p.Start.Format(dateLayout),
		"To":        p.End.AddDate(0, 0, -1).Format(dateLayout),
		"Chips":     chips,
		"Tiles":     tiles(cur, prev),
		"Chart":     template.JS(chartJSON),
		"Rows":      rows,
		"Cards":     cards,
		"SaltNote":  saltNote(site, p),
		"Empty":     cur.Views == 0 && cur.Visitors == 0,
	})
}

func (s *Server) panel(c *ctx) {
	site := s.siteFor(c)
	if site == nil {
		return
	}
	q := c.r.URL.Query()
	dim := q.Get("dim")
	if dim != "entry" && !store.ValidDimension(dim) {
		http.Error(c.w, "unknown dimension", http.StatusBadRequest)
		return
	}
	limit := panelLimit
	if q.Get("all") == "1" {
		limit = 200
	}
	p := parsePeriod(q, site.Location(), s.Now())
	pv, err := s.buildPanel(site, p, parseFilters(q), dim, limit)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.render(c.w, http.StatusOK, "panel", pv)
}

func (s *Server) eventProps(c *ctx) {
	site := s.siteFor(c)
	if site == nil {
		return
	}
	q := c.r.URL.Query()
	name := q.Get("name")
	p := parsePeriod(q, site.Location(), s.Now())
	props, err := s.store.EventProps(p.query(site.ID, parseFilters(q)), name, 10)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.render(c.w, http.StatusOK, "props", map[string]any{"Name": name, "Props": props})
}

type minuteBar struct {
	Label  string
	Count  int64
	Height float64 // percent of the tallest bar
}

func (s *Server) realtimeView(site *store.Site) (map[string]any, error) {
	now := s.Now()
	rt, err := s.store.Realtime(site.ID, now.Unix())
	if err != nil {
		return nil, err
	}
	var bars []minuteBar
	var peak, total int64
	current := now.Unix() / 60
	for m := current - 29; m <= current; m++ {
		n := rt.Minutes[m]
		if n > peak {
			peak = n
		}
		total += n
		bars = append(bars, minuteBar{Label: time.Unix(m*60, 0).In(site.Location()).Format("15:04"), Count: n})
	}
	for i := range bars {
		if peak > 0 {
			bars[i].Height = float64(bars[i].Count) / float64(peak) * 100
		}
	}
	type recent struct {
		Time, What, Path, Country, Browser, Referrer string
	}
	var feed []recent
	for _, r := range rt.Recent {
		what := "Page view"
		if r.Kind == store.KindCustom {
			what = r.Name
		}
		feed = append(feed, recent{time.Unix(r.TS, 0).In(site.Location()).Format("15:04:05"),
			what, r.Path, r.Country, r.Browser, r.RefHost})
	}
	return map[string]any{
		"Site": site, "Active": rt.Active, "Bars": bars, "Peak": peak, "Total": total, "Recent": feed,
		"First": bars[0].Label, "Last": bars[len(bars)-1].Label,
	}, nil
}

func (s *Server) realtime(c *ctx) {
	site := s.siteFor(c)
	if site == nil {
		return
	}
	data, err := s.realtimeView(site)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	data["Base"] = s.base(c, site.Name+" · Realtime")
	data["Nav"] = nav(site, "realtime")
	s.render(c.w, http.StatusOK, "realtime.html", data)
}

func (s *Server) realtimeData(c *ctx) {
	site := s.siteFor(c)
	if site == nil {
		return
	}
	data, err := s.realtimeView(site)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.render(c.w, http.StatusOK, "realtime-data", data)
}

func (s *Server) setup(c *ctx) {
	site := s.siteFor(c)
	if site == nil {
		return
	}
	public := s.public
	if public == "" {
		public = "https://stats.example.com"
	}
	saltLabels := map[string]string{"day": "One day at a time", "week": "One week at a time", "month": "One month at a time"}
	saltLabel := saltLabels[site.SaltInterval]
	if saltLabel == "" {
		saltLabel = strings.TrimSuffix(site.SaltInterval, "d") + " days at a time"
	}
	s.render(c.w, http.StatusOK, "setup.html", map[string]any{
		"Base": s.base(c, site.Name+" · Setup"), "Site": site, "Nav": nav(site, "setup"),
		"PublicURL": public, "HasPublicURL": s.public != "", "SaltLabel": saltLabel,
	})
}

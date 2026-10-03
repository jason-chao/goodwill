// Package ingest receives events over HTTP. It accepts the request format of
// the Umami tracker (POST /api/send), so that script and its client libraries
// work unchanged.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"zgo.at/isbot"

	"github.com/jason-chao/goodwill/internal/config"
	"github.com/jason-chao/goodwill/internal/enrich"
	"github.com/jason-chao/goodwill/internal/geo"
	"github.com/jason-chao/goodwill/internal/identity"
	"github.com/jason-chao/goodwill/internal/store"
)

const (
	maxBatchEvents = 500
	maxBatchBytes  = 2 << 20
	// maxClockSkew is how far ahead of the server clock a supplied
	// timestamp may be.
	maxClockSkew = 300
)

// Handler serves the public endpoints.
type Handler struct {
	store   *store.Store
	ident   *identity.Manager
	geo     *geo.DB
	proxies []netip.Prefix
	header  string
	maxBody int64
	limit   *limiter
	tracker []byte
	etag    string

	// Now is the clock; tests replace it.
	Now func() time.Time
}

func New(cfg config.Config, st *store.Store, ident *identity.Manager, g *geo.DB, tracker []byte) (*Handler, error) {
	proxies, err := config.ParsePrefixes(cfg.Server.TrustedProxies)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(tracker)
	return &Handler{
		store:   st,
		ident:   ident,
		geo:     g,
		proxies: proxies,
		header:  cfg.Server.ClientIPHeader,
		maxBody: cfg.Limits.MaxBodyBytes,
		limit:   &limiter{perMinute: cfg.Limits.EventsPerMinute, counts: map[netip.Addr]int{}},
		tracker: tracker,
		etag:    `"` + hex.EncodeToString(sum[:8]) + `"`,
		Now:     time.Now,
	}, nil
}

// Routes returns the public mux.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /script.js", h.script)
	mux.HandleFunc("GET /api/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("OPTIONS /api/send", preflight)
	mux.HandleFunc("OPTIONS /api/batch", preflight)
	mux.HandleFunc("POST /api/send", h.send)
	mux.HandleFunc("POST /api/batch", h.batch)
	return mux
}

func (h *Handler) script(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("Content-Type", "application/javascript; charset=utf-8")
	hd.Set("Cache-Control", "public, max-age=86400")
	hd.Set("Cross-Origin-Resource-Policy", "cross-origin")
	hd.Set("ETag", h.etag)
	if r.Header.Get("If-None-Match") == h.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(h.tracker)
}

// The tracker posts JSON with custom headers from other origins, so browsers
// ask permission first.
func cors(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Access-Control-Allow-Origin", "*")
	hd.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	hd.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-umami-cache, x-umami-website-id, x-umami-hostname")
	hd.Set("Access-Control-Max-Age", "86400")
}

func preflight(w http.ResponseWriter, r *http.Request) {
	cors(w)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type envelope struct {
	Type    string  `json:"type"`
	Payload payload `json:"payload"`
}

type payload struct {
	Website   string          `json:"website"`
	Hostname  string          `json:"hostname"`
	Language  string          `json:"language"`
	Referrer  string          `json:"referrer"`
	Screen    string          `json:"screen"`
	Title     string          `json:"title"`
	URL       string          `json:"url"`
	Name      string          `json:"name"`
	Tag       string          `json:"tag"`
	ID        string          `json:"id"`
	Data      map[string]any  `json:"data"`
	IP        string          `json:"ip"`
	UserAgent string          `json:"userAgent"`
	Timestamp json.RawMessage `json:"timestamp"`
	LCP       *float64        `json:"lcp"`
	INP       *float64        `json:"inp"`
	CLS       *float64        `json:"cls"`
	FCP       *float64        `json:"fcp"`
	TTFB      *float64        `json:"ttfb"`
}

// request is what one HTTP request contributes to every event it carries.
type request struct {
	remote    netip.Addr
	userAgent string
	origin    string
	key       string
	websiteID string
	isBot     bool
}

type result struct {
	status  int
	message string
}

var accepted = result{http.StatusOK, ""}

func (h *Handler) newRequest(r *http.Request) *request {
	rq := &request{
		remote:    h.clientIP(r),
		userAgent: r.UserAgent(),
		origin:    r.Header.Get("Origin"),
		websiteID: r.Header.Get("x-umami-website-id"),
		isBot:     isbot.Is(isbot.Bot(r)),
	}
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		rq.key = strings.TrimSpace(auth[7:])
	}
	return rq
}

func (h *Handler) send(w http.ResponseWriter, r *http.Request) {
	cors(w)
	var env envelope
	if err := decodeBody(w, r, h.maxBody, &env); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	res := h.process(h.newRequest(r), &env, false)
	if res.status != http.StatusOK {
		writeJSON(w, res.status, map[string]any{"message": res.message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) batch(w http.ResponseWriter, r *http.Request) {
	cors(w)
	var envs []envelope
	if err := decodeBody(w, r, maxBatchBytes, &envs); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if len(envs) > maxBatchEvents {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "a batch holds at most 500 events"})
		return
	}
	rq := h.newRequest(r)
	type failure struct {
		Index   int    `json:"index"`
		Status  int    `json:"status"`
		Message string `json:"message"`
	}
	details := []failure{}
	for i := range envs {
		if res := h.process(rq, &envs[i], true); res.status != http.StatusOK {
			details = append(details, failure{i, res.status, res.message})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"size":      len(envs),
		"processed": len(envs) - len(details),
		"errors":    len(details),
		"details":   details,
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return errors.New("request body is too large or unreadable")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return errors.New("request body is not valid JSON of the expected shape")
	}
	return nil
}

// process validates one event and queues it. A 200 result does not always
// mean the event was stored: bots and ignored addresses are dropped silently
// so that they learn nothing from the response.
func (h *Handler) process(rq *request, env *envelope, batch bool) result {
	p := &env.Payload
	typ := env.Type
	if typ == "" {
		typ = "event"
	}
	if typ != "event" && typ != "identify" && typ != "performance" {
		return result{http.StatusBadRequest, "unknown event type"}
	}

	websiteID := p.Website
	if websiteID == "" {
		websiteID = rq.websiteID
	}
	if len(websiteID) != 36 {
		return result{http.StatusBadRequest, "Website not found."}
	}
	site, err := h.store.SiteByPublicID(websiteID)
	if err != nil {
		slog.Error("site lookup failed", "err", err)
		return result{http.StatusInternalServerError, "internal error"}
	}
	if site == nil {
		return result{http.StatusBadRequest, "Website not found."}
	}

	// A trusted sender is one of the site's own servers: it holds the ingest
	// key or connects from a listed address. Only a trusted sender may speak
	// for someone else by supplying an address, user agent or timestamp.
	trusted := site.CheckIngestKey(rq.key)
	if !trusted && len(site.Settings.TrustedSources) > 0 {
		if sources, err := config.ParsePrefixes(site.Settings.TrustedSources); err == nil {
			trusted = config.Contains(sources, rq.remote)
		}
	}
	if batch && !trusted {
		return result{http.StatusUnauthorized, "batch requests need an ingest key"}
	}

	now := h.Now()
	if !trusted {
		if !h.limit.allow(rq.remote, now.Unix()) {
			return result{http.StatusTooManyRequests, "too many requests"}
		}
		if len(site.Domains) > 0 {
			if rq.origin != "" && rq.origin != "null" {
				if u, err := url.Parse(rq.origin); err != nil || !hostAllowed(site.Domains, u.Host) {
					return result{http.StatusForbidden, "origin is not allowed for this website"}
				}
			}
			if !hostAllowed(site.Domains, p.Hostname) {
				return result{http.StatusForbidden, "hostname is not allowed for this website"}
			}
		}
	}

	addr, userAgent, ts := rq.remote, rq.userAgent, now.Unix()
	if trusted {
		if p.IP != "" {
			a, err := netip.ParseAddr(strings.TrimSpace(p.IP))
			if err != nil {
				return result{http.StatusBadRequest, "ip is not a valid address"}
			}
			addr = a.Unmap()
		}
		if p.UserAgent != "" {
			userAgent = p.UserAgent
		}
		if len(p.Timestamp) > 0 && string(p.Timestamp) != "null" {
			v, err := strconv.ParseInt(strings.Trim(string(p.Timestamp), `"`), 10, 64)
			if err != nil || v <= 0 {
				return result{http.StatusBadRequest, "timestamp must be unix seconds"}
			}
			if v > now.Unix()+maxClockSkew {
				return result{http.StatusBadRequest, "timestamp is in the future"}
			}
			ts = v
		}
	} else if rq.isBot {
		return accepted
	}

	if len(site.Settings.IgnoreIPs) > 0 {
		if ignored, err := config.ParsePrefixes(site.Settings.IgnoreIPs); err == nil && config.Contains(ignored, addr) {
			return accepted
		}
	}

	if formulaTrigger(p.Name) || formulaTrigger(p.Tag) {
		return result{http.StatusBadRequest, "name and tag must not start with =, +, -, @, tab or carriage return"}
	}

	collect := site.Settings.Collect
	when := time.Unix(ts, 0).In(site.Location())
	y, m, d := when.Date()
	ev := &store.Event{
		SiteID:    site.ID,
		TS:        ts,
		LocalDay:  y*10000 + int(m)*100 + d,
		LocalHour: when.Hour(),
		Tag:       enrich.Truncate(p.Tag, 50),
	}

	page := enrich.ParsePage(p.URL, p.Hostname, site.Settings.KeepQuery)
	ev.Host, ev.Path = page.Host, page.Path
	if p.URL == "" {
		// Server-side events often have no page at all.
		ev.Path = ""
	}
	ev.Title = enrich.Truncate(p.Title, 500)

	var ref enrich.Referrer
	if collect.Referrer {
		ref = enrich.ParseReferrer(p.Referrer, page.Host)
	}

	switch typ {
	case "event":
		if p.Name != "" {
			ev.Kind = store.KindCustom
			ev.Name = enrich.Truncate(p.Name, 50)
			if collect.Props {
				ev.Props = enrich.Props(p.Data)
			}
		} else {
			ev.Kind = store.KindPageview
			ev.RefHost, ev.RefPath = ref.Host, ref.Path
		}
		if collect.UTM {
			ev.UTMSource, ev.UTMMedium, ev.UTMCampaign = page.UTMSource, page.UTMMedium, page.UTMCampaign
			ev.UTMContent, ev.UTMTerm = page.UTMContent, page.UTMTerm
		}
	case "identify":
		ev.Kind = store.KindIdentify
		ev.Path, ev.Title = "", ""
		if collect.Props {
			ev.Props = enrich.Props(p.Data)
		}
		if !collect.Identify && len(ev.Props) == 0 {
			return accepted
		}
	case "performance":
		if !collect.Performance {
			return accepted
		}
		ev.Kind = store.KindPerformance
		ev.LCP, ev.INP = bounded(p.LCP, 60000), bounded(p.INP, 60000)
		ev.FCP, ev.TTFB = bounded(p.FCP, 60000), bounded(p.TTFB, 60000)
		ev.CLS = bounded(p.CLS, 100)
	}

	if collect.Identify {
		ev.DistinctID = enrich.Truncate(p.ID, 50)
	}
	if collect.UserAgent {
		a := enrich.ParseAgent(userAgent)
		ev.Browser, ev.OS, ev.Device = a.Browser, a.OS, a.Device
	}
	if collect.Screen {
		ev.Screen = enrich.ScreenBucket(p.Screen)
	}
	if collect.Language {
		ev.Lang = enrich.Language(p.Language)
	}
	if collect.Location != "none" {
		loc := h.geo.Lookup(addr)
		ev.Country = loc.Country
		if collect.Location == "region" || collect.Location == "city" {
			ev.Region = loc.Region
		}
		if collect.Location == "city" {
			ev.City = loc.City
		}
	}

	id, err := h.ident.Identify(site, addr, userAgent, ts, now)
	if errors.Is(err, identity.ErrBeforeSalt) {
		return result{http.StatusBadRequest, "timestamp is older than the current salt interval"}
	}
	if err != nil {
		slog.Error("identify failed", "err", err)
		return result{http.StatusInternalServerError, "internal error"}
	}
	ev.Visitor, ev.Visit = id.Visitor, id.Visit

	// How a visit arrived is recorded once, on the page view that starts it.
	if ev.Kind == store.KindPageview && (id.NewVisit || ref.Host != "") {
		ev.Channel = enrich.Channel(ref.Host, ev.UTMSource, ev.UTMMedium)
	}

	if err := h.store.Enqueue(ev); err != nil {
		return result{http.StatusServiceUnavailable, "busy, try again shortly"}
	}
	return accepted
}

func bounded(v *float64, max float64) *float64 {
	if v == nil || *v < 0 || *v > max {
		return nil
	}
	return v
}

// formulaTrigger reports whether s would be run as a formula if a
// spreadsheet opened it from an export.
func formulaTrigger(s string) bool {
	return s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0]))
}

// hostAllowed reports whether host is on the site's list. An entry matches
// itself with or without "www."; "*.example.com" also matches subdomains.
func hostAllowed(domains []string, host string) bool {
	host = enrich.Host(host)
	if host == "" {
		return false
	}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if rest, ok := strings.CutPrefix(d, "*."); ok {
			if host == rest || strings.HasSuffix(host, "."+rest) {
				return true
			}
			continue
		}
		if host == strings.TrimPrefix(d, "www.") {
			return true
		}
	}
	return false
}

// clientIP returns the visitor's address. The proxy header is believed only
// when the connection comes from a trusted proxy, and is read from the right
// so that values a client put there itself are ignored.
func (h *Handler) clientIP(r *http.Request) netip.Addr {
	var remote netip.Addr
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		remote = ap.Addr().Unmap()
	}
	if !remote.IsValid() || !config.Contains(h.proxies, remote) || h.header == "" {
		return remote
	}
	parts := strings.Split(strings.Join(r.Header.Values(h.header), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			continue
		}
		a = a.Unmap()
		if !config.Contains(h.proxies, a) {
			return a
		}
	}
	return remote
}

// limiter counts events per address in one-minute windows. Addresses are
// held in memory only and forgotten when the window turns over.
type limiter struct {
	mu        sync.Mutex
	perMinute int
	window    int64
	counts    map[netip.Addr]int
}

func (l *limiter) allow(addr netip.Addr, now int64) bool {
	if l.perMinute <= 0 {
		return true
	}
	if addr.Is6() {
		// One household or device usually holds a whole /64.
		if p, err := addr.Prefix(64); err == nil {
			addr = p.Addr()
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := now / 60; w != l.window {
		l.window = w
		l.counts = map[netip.Addr]int{}
	}
	l.counts[addr]++
	return l.counts[addr] <= l.perMinute
}

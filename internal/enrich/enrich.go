// Package enrich derives the coarse descriptors stored with an event from
// the raw request: browser family, screen bucket, referrer, channel and so on.
package enrich

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	useragent "github.com/medama-io/go-useragent"
)

var uaParser = useragent.NewParser()

// Agent is what is kept of a user agent string.
type Agent struct {
	Browser string
	OS      string
	Device  string // desktop, mobile, tablet or tv
}

// ParseAgent reduces a user agent string to browser, OS and device type.
func ParseAgent(ua string) Agent {
	if ua == "" {
		return Agent{}
	}
	p := uaParser.Parse(ua)
	a := Agent{Browser: string(p.Browser()), OS: string(p.OS())}
	switch {
	case p.IsTablet():
		a.Device = "tablet"
	case p.IsMobile():
		a.Device = "mobile"
	case p.IsTV():
		a.Device = "tv"
	case p.IsDesktop():
		a.Device = "desktop"
	}
	return a
}

// ScreenBucket maps a "<width>x<height>" screen size to a coarse width
// class, so that exact dimensions are never stored.
func ScreenBucket(screen string) string {
	w, _, _ := strings.Cut(screen, "x")
	n, err := strconv.Atoi(strings.TrimSpace(w))
	if err != nil || n <= 0 {
		return ""
	}
	switch {
	case n < 480:
		return "xs"
	case n < 768:
		return "sm"
	case n < 1024:
		return "md"
	case n < 1280:
		return "lg"
	case n < 1536:
		return "xl"
	default:
		return "2xl"
	}
}

var langRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})?`)

// Language normalises a browser language tag to "en" or "en-GB" form.
func Language(tag string) string {
	m := langRe.FindString(strings.TrimSpace(tag))
	if m == "" {
		return ""
	}
	lang, region, ok := strings.Cut(m, "-")
	if !ok {
		return strings.ToLower(lang)
	}
	if len(region) == 2 {
		region = strings.ToUpper(region)
	}
	return strings.ToLower(lang) + "-" + region
}

// Truncate shortens s to at most n characters without splitting one.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for count := 0; count < n; count++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

// Host lower-cases a hostname and removes any port and leading "www.".
func Host(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.TrimPrefix(h, "www.")
}

// Page is what is kept of the URL an event happened on.
type Page struct {
	Host        string
	Path        string
	UTMSource   string
	UTMMedium   string
	UTMCampaign string
	UTMContent  string
	UTMTerm     string
}

// ParsePage splits a URL (absolute or a path) reported for hostname. The
// query string is discarded apart from campaign parameters and any keys in
// keepQuery, which stay attached to the path. A fragment is kept only when it
// is used for routing ("#/...").
func ParsePage(rawURL, hostname string, keepQuery []string) Page {
	base := &url.URL{Scheme: "https", Host: "localhost"}
	if h := Host(hostname); h != "" {
		base.Host = h
	}
	u, err := base.Parse(rawURL)
	if err != nil {
		return Page{Host: Host(hostname)}
	}
	p := Page{Host: Host(u.Host), Path: u.EscapedPath()}
	if hostname == "" && u.Host == "localhost" {
		p.Host = ""
	}
	if dec, err := url.PathUnescape(p.Path); err == nil && utf8.ValidString(dec) {
		p.Path = dec
	}
	if p.Path == "" {
		p.Path = "/"
	}
	q := u.Query()
	if len(keepQuery) > 0 {
		kept := url.Values{}
		for _, k := range keepQuery {
			if v, ok := q[k]; ok {
				kept[k] = v
			}
		}
		if len(kept) > 0 {
			p.Path += "?" + kept.Encode()
		}
	}
	if strings.HasPrefix(u.Fragment, "/") {
		p.Path += "#" + u.Fragment
	}
	p.Path = Truncate(p.Path, 500)
	p.UTMSource = Truncate(q.Get("utm_source"), 255)
	p.UTMMedium = Truncate(q.Get("utm_medium"), 255)
	p.UTMCampaign = Truncate(q.Get("utm_campaign"), 255)
	p.UTMContent = Truncate(q.Get("utm_content"), 255)
	p.UTMTerm = Truncate(q.Get("utm_term"), 255)
	return p
}

// Referrer is what is kept of the page a visitor came from.
type Referrer struct {
	Host string
	Path string
}

// ParseReferrer returns the external referrer, or a zero value when the
// referrer is missing, invalid, or on the site itself.
func ParseReferrer(raw, pageHost string) Referrer {
	if raw == "" {
		return Referrer{}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "android-app") {
		return Referrer{}
	}
	h := Host(u.Host)
	if h == "" || h == pageHost {
		return Referrer{}
	}
	path := u.EscapedPath()
	if path == "/" {
		path = ""
	}
	return Referrer{Host: Truncate(h, 255), Path: Truncate(path, 500)}
}

var (
	searchHosts = []string{"google.", "bing.com", "duckduckgo.com", "yahoo.", "baidu.com", "yandex.",
		"ecosia.org", "search.brave.com", "kagi.com", "startpage.com", "qwant.com", "naver.com", "seznam.cz"}
	socialHosts = []string{"facebook.com", "fb.com", "t.co", "twitter.com", "x.com", "linkedin.com", "lnkd.in",
		"instagram.com", "reddit.com", "youtube.com", "youtu.be", "tiktok.com", "pinterest.", "threads.net",
		"threads.com", "bsky.app", "news.ycombinator.com", "lobste.rs", "whatsapp.com", "t.me", "telegram.org",
		"weibo.com", "vk.com", "discord.com", "snapchat.com"}
	aiHosts = []string{"chatgpt.com", "chat.openai.com", "perplexity.ai", "gemini.google.com",
		"copilot.microsoft.com", "chat.mistral.ai", "chat.deepseek.com", "you.com", "phind.com"}
	paidMediums = map[string]bool{"cpc": true, "ppc": true, "paid": true, "paidsearch": true, "paid-search": true,
		"paidsocial": true, "paid-social": true, "display": true, "cpm": true, "banner": true, "ads": true, "ad": true}
)

func hostMatches(host string, list []string) bool {
	for _, p := range list {
		if strings.HasSuffix(p, ".") {
			// "google." matches google.com, google.co.uk, www.google.de ...
			if strings.HasPrefix(host, p) || strings.Contains(host, "."+p) {
				return true
			}
			continue
		}
		if host == p || strings.HasSuffix(host, "."+p) {
			return true
		}
	}
	return false
}

// Channel classifies how a visit arrived.
func Channel(refHost, utmSource, utmMedium string) string {
	medium := strings.ToLower(utmMedium)
	switch {
	case paidMediums[medium]:
		return "paid"
	case medium == "email" || medium == "newsletter":
		return "email"
	case medium == "social":
		return "social"
	}
	if refHost == "" {
		if utmSource != "" || utmMedium != "" {
			return "campaign"
		}
		return "direct"
	}
	switch {
	case hostMatches(refHost, aiHosts):
		return "ai"
	case hostMatches(refHost, searchHosts):
		return "search"
	case hostMatches(refHost, socialHosts):
		return "social"
	case strings.HasPrefix(refHost, "mail.") || strings.Contains(refHost, "webmail"):
		return "email"
	}
	return "referral"
}

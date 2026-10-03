package enrich

import (
	"reflect"
	"strings"
	"testing"
)

func TestScreenBucket(t *testing.T) {
	for in, want := range map[string]string{
		"375x812": "xs", "479x1": "xs", "480x800": "sm", "768x1024": "md", "1024x768": "lg",
		"1280x720": "xl", "1536x864": "2xl", "3840x2160": "2xl", "": "", "x": "", "-5x9": "", "abc": "",
	} {
		if got := ScreenBucket(in); got != want {
			t.Errorf("ScreenBucket(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"en-GB": "en-GB", "EN-gb": "en-GB", "de": "de", "zh-Hant-HK": "zh-Hant", "": "", "<script>": "", "fr-FR,fr;q=0.9": "fr-FR",
	} {
		if got := Language(in); got != want {
			t.Errorf("Language(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePage(t *testing.T) {
	cases := []struct {
		url, host string
		keep      []string
		want      Page
	}{
		{"/pricing?utm_source=news&utm_medium=email&secret=1", "www.Example.com", nil,
			Page{Host: "example.com", Path: "/pricing", UTMSource: "news", UTMMedium: "email"}},
		{"https://example.com/a%20b?x=1#top", "example.com", nil, Page{Host: "example.com", Path: "/a b"}},
		{"/app#/settings/profile", "example.com", nil, Page{Host: "example.com", Path: "/app#/settings/profile"}},
		{"/search?q=shoes&page=2&token=abc", "example.com", []string{"q", "page"},
			Page{Host: "example.com", Path: "/search?page=2&q=shoes"}},
		{"", "example.com", nil, Page{Host: "example.com", Path: "/"}},
		{"/only-path", "", nil, Page{Path: "/only-path"}},
	}
	for _, c := range cases {
		if got := ParsePage(c.url, c.host, c.keep); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParsePage(%q, %q) = %+v, want %+v", c.url, c.host, got, c.want)
		}
	}
	if got := ParsePage("/"+strings.Repeat("é", 600), "example.com", nil); len([]rune(got.Path)) != 500 {
		t.Errorf("long path kept %d characters, want 500", len([]rune(got.Path)))
	}
}

func TestParseReferrer(t *testing.T) {
	cases := []struct {
		raw, host string
		want      Referrer
	}{
		{"https://www.google.com/", "example.com", Referrer{Host: "google.com"}},
		{"https://news.example.org/item?id=1", "example.com", Referrer{Host: "news.example.org", Path: "/item"}},
		{"https://www.example.com/pricing", "example.com", Referrer{}},
		{"/pricing", "example.com", Referrer{}},
		{"", "example.com", Referrer{}},
		{"javascript:alert(1)", "example.com", Referrer{}},
	}
	for _, c := range cases {
		if got := ParseReferrer(c.raw, c.host); got != c.want {
			t.Errorf("ParseReferrer(%q) = %+v, want %+v", c.raw, got, c.want)
		}
	}
}

func TestChannel(t *testing.T) {
	cases := []struct{ ref, source, medium, want string }{
		{"", "", "", "direct"},
		{"google.com", "", "", "search"},
		{"google.co.uk", "", "", "search"},
		{"gemini.google.com", "", "", "ai"},
		{"notgoogle.com", "", "", "referral"},
		{"t.co", "", "", "social"},
		{"old.reddit.com", "", "", "social"},
		{"chatgpt.com", "", "", "ai"},
		{"blog.example.org", "", "", "referral"},
		{"google.com", "google", "cpc", "paid"},
		{"", "newsletter", "email", "email"},
		{"", "partner", "", "campaign"},
	}
	for _, c := range cases {
		if got := Channel(c.ref, c.source, c.medium); got != c.want {
			t.Errorf("Channel(%q, %q, %q) = %q, want %q", c.ref, c.source, c.medium, got, c.want)
		}
	}
}

func TestProps(t *testing.T) {
	got := Props(map[string]any{
		"plan":                  "pro",
		"seats":                 float64(3),
		"trial":                 true,
		"empty":                 "",
		"nothing":               nil,
		"nested":                map[string]any{"tier": "gold", "deep": map[string]any{"n": float64(1)}},
		"tags":                  []any{"a", "b"},
		"long":                  strings.Repeat("x", 300),
		strings.Repeat("k", 65): "dropped",
	})
	want := map[string]any{
		"plan": "pro", "seats": float64(3), "trial": true, "nested.tier": "gold", "nested.deep.n": float64(1),
		"tags": `["a","b"]`, "long": strings.Repeat("x", 255),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Props = %#v\nwant %#v", got, want)
	}

	many := map[string]any{}
	for i := 0; i < 40; i++ {
		many[string(rune('a'+i/26))+string(rune('a'+i%26))] = float64(i)
	}
	if n := len(Props(many)); n != MaxProps {
		t.Errorf("kept %d properties, want %d", n, MaxProps)
	}
	if Props(nil) != nil || Props(map[string]any{"x": nil}) != nil {
		t.Error("empty input should give nil")
	}
}

func TestParseAgent(t *testing.T) {
	a := ParseAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
	if a.Browser != "Safari" || a.OS != "iOS" || a.Device != "mobile" {
		t.Errorf("iPhone agent parsed as %+v", a)
	}
	if got := ParseAgent(""); got != (Agent{}) {
		t.Errorf("empty agent parsed as %+v", got)
	}
}

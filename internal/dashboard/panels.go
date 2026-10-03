package dashboard

import (
	"net/url"

	"github.com/jason-chao/goodwill/internal/store"
)

const panelLimit = 10

type tabDef struct {
	Dim, Label string
}

// cardDef is one card of breakdowns on the site page.
type cardDef struct {
	ID    string
	Title string
	Tabs  []tabDef
}

var cardDefs = []cardDef{
	{"pages", "Pages", []tabDef{{"path", "Pages"}, {"entry", "Entry pages"}}},
	{"sources", "Sources", []tabDef{{"referrer", "Referrers"}, {"channel", "Channels"},
		{"utm_source", "UTM source"}, {"utm_medium", "UTM medium"}, {"utm_campaign", "UTM campaign"}}},
	{"locations", "Locations", []tabDef{{"country", "Countries"}, {"region", "Regions"}, {"city", "Cities"}}},
	{"tech", "Technology", []tabDef{{"browser", "Browsers"}, {"os", "Operating systems"},
		{"device", "Devices"}, {"screen", "Screens"}, {"lang", "Languages"}}},
	{"events", "Events", []tabDef{{"event", "Events"}}},
}

type cardView struct {
	Def   cardDef
	Panel panelView
}

func cardOf(dim string) (cardDef, tabDef) {
	for _, c := range cardDefs {
		for _, t := range c.Tabs {
			if t.Dim == dim {
				return c, t
			}
		}
	}
	return cardDef{}, tabDef{}
}

func dimLabel(dim string) string {
	labels := map[string]string{
		"path": "Page", "host": "Host", "referrer": "Referrer", "channel": "Channel", "event": "Event",
		"utm_source": "UTM source", "utm_medium": "UTM medium", "utm_campaign": "UTM campaign",
		"country": "Country", "region": "Region", "city": "City", "browser": "Browser",
		"os": "Operating system", "device": "Device", "screen": "Screen", "lang": "Language", "tag": "Tag",
	}
	return labels[dim]
}

var valueLabels = map[string]map[string]string{
	"screen": {
		"xs": "Extra small (under 480 px)", "sm": "Small (480 to 767 px)", "md": "Medium (768 to 1023 px)",
		"lg": "Large (1024 to 1279 px)", "xl": "Extra large (1280 to 1535 px)", "2xl": "Widest (1536 px and up)",
	},
	"channel": {
		"direct": "Direct", "search": "Search", "social": "Social", "referral": "Other sites", "email": "Email",
		"paid": "Paid", "ai": "AI assistants", "campaign": "Campaign",
	},
	"device": {"desktop": "Desktop", "mobile": "Phone", "tablet": "Tablet", "tv": "TV"},
}

type tabView struct {
	Label  string
	URL    string
	Active bool
}

type panelRow struct {
	Label    string
	Country  string // ISO code, when the row is a country
	Visitors int64
	Count    int64
	Pct      float64
	URL      string
	PropsURL string
}

// panelView is one breakdown: the tabs of its card and the rows of the
// selected tab.
type panelView struct {
	SiteID      int64
	CardID      string
	Title       string
	Tabs        []tabView
	Header      string
	CountHeader string // empty hides the second number
	Rows        []panelRow
	MoreURL     string
	Active      string // value of the current filter on this dimension, if any
}

func (s *Server) buildPanel(site *store.Site, p period, filters []store.Filter, dim string, limit int) (panelView, error) {
	card, tab := cardOf(dim)
	rows, err := s.store.Breakdown(p.query(site.ID, filters), dim, limit+1)
	if err != nil {
		return panelView{}, err
	}
	base := "/sites/" + itoa(site.ID)
	qs := queryString(p, filters, "", "")
	pv := panelView{SiteID: site.ID, CardID: card.ID, Title: card.Title, Header: tab.Label}
	for _, t := range card.Tabs {
		pv.Tabs = append(pv.Tabs, tabView{t.Label, base + "/panel?dim=" + t.Dim + "&" + qs, t.Dim == dim})
	}
	switch dim {
	case "path":
		pv.Header, pv.CountHeader = "Page", "Views"
	case "entry":
		pv.Header, pv.CountHeader = "Entry page", "Visits"
	case "event":
		pv.Header, pv.CountHeader = "Event", "Events"
	}
	if len(rows) > limit {
		rows = rows[:limit]
		pv.MoreURL = base + "/panel?dim=" + dim + "&all=1&" + qs
	}
	filterDim := dim
	if dim == "entry" {
		filterDim = "path"
	}
	for _, f := range filters {
		if f.Dim == filterDim {
			pv.Active = f.Value
		}
	}
	var top int64
	for _, r := range rows {
		if r.Visitors > top {
			top = r.Visitors
		}
	}
	for _, r := range rows {
		row := panelRow{Label: r.Value, Visitors: r.Visitors, Count: r.Count}
		value := r.Value
		if value == "" {
			row.Label, value = "Unknown", store.None
		} else if l, ok := valueLabels[dim][r.Value]; ok {
			row.Label = l
		}
		if dim == "country" && r.Value != "" {
			row.Country = r.Value
		}
		if top > 0 {
			row.Pct = float64(r.Visitors) / float64(top) * 100
		}
		row.URL = base + "?" + queryString(p, filters, filterDim, value)
		if dim == "event" {
			row.PropsURL = base + "/event?name=" + url.QueryEscape(r.Value) + "&" + qs
		}
		pv.Rows = append(pv.Rows, row)
	}
	return pv, nil
}

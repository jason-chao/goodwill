package dashboard

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jason-chao/goodwill/internal/store"
)

// period is the date range a dashboard page covers, as whole days in the
// site's timezone.
type period struct {
	Key    string
	Label  string
	Start  time.Time // first instant of the first day
	End    time.Time // first instant of the day after the last
	Hourly bool
}

type periodOption struct {
	Key, Label string
}

var periodOptions = []periodOption{
	{"today", "Today"},
	{"yesterday", "Yesterday"},
	{"7d", "Last 7 days"},
	{"30d", "Last 30 days"},
	{"90d", "Last 90 days"},
	{"12m", "Last 12 months"},
}

const dateLayout = "2006-01-02"

func parsePeriod(q url.Values, loc *time.Location, now time.Time) period {
	now = now.In(loc)
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	tomorrow := today.AddDate(0, 0, 1)

	key := q.Get("period")
	if key == "custom" {
		from, err1 := time.ParseInLocation(dateLayout, q.Get("from"), loc)
		to, err2 := time.ParseInLocation(dateLayout, q.Get("to"), loc)
		if err1 == nil && err2 == nil && !to.Before(from) && to.Sub(from) < 5*366*24*time.Hour {
			p := period{Key: "custom", Start: from, End: to.AddDate(0, 0, 1)}
			p.Label = from.Format("2 Jan 2006") + " to " + to.Format("2 Jan 2006")
			p.Hourly = p.days() <= 2
			return p
		}
		key = ""
	}
	p := period{Key: key, End: tomorrow}
	switch key {
	case "today":
		p.Start = today
	case "yesterday":
		p.Start, p.End = today.AddDate(0, 0, -1), today
	case "30d":
		p.Start = today.AddDate(0, 0, -29)
	case "90d":
		p.Start = today.AddDate(0, 0, -89)
	case "12m":
		p.Start = today.AddDate(-1, 0, 1)
	default:
		p.Key = "7d"
		p.Start = today.AddDate(0, 0, -6)
	}
	for _, o := range periodOptions {
		if o.Key == p.Key {
			p.Label = o.Label
		}
	}
	p.Hourly = p.days() <= 2
	return p
}

// days is the number of calendar days covered.
func (p period) days() int {
	return int(p.End.Sub(p.Start).Hours()/24 + 0.5)
}

// previous is the period of the same length immediately before this one.
func (p period) previous() period {
	n := p.days()
	return period{Start: p.Start.AddDate(0, 0, -n), End: p.Start, Hourly: p.Hourly}
}

func (p period) query(siteID int64, filters []store.Filter) store.Query {
	return store.Query{SiteID: siteID, From: p.Start.Unix(), To: p.End.Unix(), Filters: filters}
}

// values returns the URL parameters that reproduce the period.
func (p period) values() url.Values {
	v := url.Values{}
	v.Set("period", p.Key)
	if p.Key == "custom" {
		v.Set("from", p.Start.Format(dateLayout))
		v.Set("to", p.End.AddDate(0, 0, -1).Format(dateLayout))
	}
	return v
}

func parseFilters(q url.Values) []store.Filter {
	var out []store.Filter
	seen := map[string]bool{}
	for _, raw := range q["f"] {
		dim, value, ok := strings.Cut(raw, ":")
		if !ok || value == "" || !store.ValidDimension(dim) || seen[dim] {
			continue
		}
		seen[dim] = true
		out = append(out, store.Filter{Dim: dim, Value: value})
	}
	return out
}

// queryString renders the period and filters, optionally adding, replacing
// or (with an empty value) removing the filter on one dimension.
func queryString(p period, filters []store.Filter, dim, value string) string {
	v := p.values()
	for _, f := range filters {
		if f.Dim != dim {
			v.Add("f", f.Dim+":"+f.Value)
		}
	}
	if dim != "" && value != "" {
		v.Add("f", dim+":"+value)
	}
	return v.Encode()
}

// saltNote explains how unique visitors add up when the period is longer
// than the interval over which a visitor can be recognised.
func saltNote(site *store.Site, p period) string {
	switch site.SaltInterval {
	case "day", "1d":
		if p.days() > 1 {
			return "Visitors are recognised for one day at a time, so someone who returns on a later day is counted again."
		}
	case "week":
		if p.days() > 7 {
			return "Visitors are recognised for one week at a time, so someone who returns in a later week is counted again."
		}
	case "month":
		if p.days() > 28 {
			return "Visitors are recognised for one month at a time, so someone who returns in a later month is counted again."
		}
	default:
		n, _ := strconv.Atoi(strings.TrimSuffix(site.SaltInterval, "d"))
		if n > 0 && p.days() > n {
			return "Visitors are recognised for " + strconv.Itoa(n) + " days at a time, so someone who returns after that is counted again."
		}
	}
	return ""
}

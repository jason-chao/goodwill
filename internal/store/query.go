package store

import (
	"fmt"
	"sort"
	"strings"
)

// Filter restricts a query to events where a dimension has a given value.
type Filter struct {
	Dim   string
	Value string
}

// Query selects a site's events in the half-open time range [From, To).
type Query struct {
	SiteID  int64
	From    int64
	To      int64
	Filters []Filter
}

// None is the filter value that matches rows where the dimension is missing.
const None = "(none)"

type dimension struct {
	col string
	// visitLevel dimensions describe how a visit arrived or what happened in
	// it. Filtering on one keeps every event of the matching visits, rather
	// than only the single row that carries the value.
	visitLevel bool
	// cond limits which rows are listed when breaking down by this dimension.
	cond string
}

var dimensions = map[string]dimension{
	"path":         {col: "path", cond: "e.kind = 1"},
	"host":         {col: "host", cond: "e.kind = 1"},
	"referrer":     {col: "ref_host", visitLevel: true, cond: "e.kind = 1 and e.ref_host is not null"},
	"channel":      {col: "channel", visitLevel: true, cond: "e.channel is not null"},
	"utm_source":   {col: "utm_source", visitLevel: true, cond: "e.utm_source is not null"},
	"utm_medium":   {col: "utm_medium", visitLevel: true, cond: "e.utm_medium is not null"},
	"utm_campaign": {col: "utm_campaign", visitLevel: true, cond: "e.utm_campaign is not null"},
	"event":        {col: "name", visitLevel: true, cond: "e.kind = 2"},
	"country":      {col: "country", cond: "e.kind in (1, 2)"},
	"region":       {col: "region", cond: "e.kind in (1, 2) and e.region is not null"},
	"city":         {col: "city", cond: "e.kind in (1, 2) and e.city is not null"},
	"browser":      {col: "browser", cond: "e.kind in (1, 2)"},
	"os":           {col: "os", cond: "e.kind in (1, 2)"},
	"device":       {col: "device", cond: "e.kind in (1, 2)"},
	"screen":       {col: "screen", cond: "e.kind in (1, 2)"},
	"lang":         {col: "lang", cond: "e.kind in (1, 2)"},
	"tag":          {col: "tag", cond: "e.kind in (1, 2) and e.tag is not null"},
}

// ValidDimension reports whether name can be used in a filter or breakdown.
// "entry" is a breakdown only.
func ValidDimension(name string) bool {
	_, ok := dimensions[name]
	return ok
}

// where builds the condition shared by every dashboard query. All column
// references use the alias e.
func (q Query) where() (string, []any) {
	var b strings.Builder
	b.WriteString("e.site_id = ? and e.ts >= ? and e.ts < ?")
	args := []any{q.SiteID, q.From, q.To}
	for _, f := range q.Filters {
		d, ok := dimensions[f.Dim]
		if !ok {
			continue
		}
		if d.visitLevel {
			b.WriteString(" and e.visit in (select s.visit from events s where s.site_id = ? and s.ts >= ? and s.ts < ? and ")
			args = append(args, q.SiteID, q.From, q.To)
			if f.Dim == "event" {
				b.WriteString("s.kind = 2 and ")
			}
			if f.Value == None {
				fmt.Fprintf(&b, "s.%s is null)", d.col)
			} else {
				fmt.Fprintf(&b, "s.%s = ?)", d.col)
				args = append(args, f.Value)
			}
			continue
		}
		if f.Value == None {
			fmt.Fprintf(&b, " and e.%s is null", d.col)
		} else {
			fmt.Fprintf(&b, " and e.%s = ?", d.col)
			args = append(args, f.Value)
		}
	}
	return b.String(), args
}

// Stats are the headline numbers for a period.
type Stats struct {
	Views    int64
	Visitors int64
	Visits   int64
	Bounces  int64
	Duration int64 // total seconds across all visits
}

// BounceRate is the share of visits with a single event, 0 to 1.
func (s Stats) BounceRate() float64 {
	if s.Visits == 0 {
		return 0
	}
	return float64(s.Bounces) / float64(s.Visits)
}

// AvgDuration is the mean visit length in seconds.
func (s Stats) AvgDuration() float64 {
	if s.Visits == 0 {
		return 0
	}
	return float64(s.Duration) / float64(s.Visits)
}

// Stats returns headline numbers. Page speed and identify records are not
// counted as activity.
func (s *Store) Stats(q Query) (Stats, error) {
	var st Stats
	w, args := q.where()
	err := s.r.QueryRow(`select coalesce(sum(e.kind = 1), 0), count(distinct e.visitor)
		from events e where `+w+` and e.kind in (1, 2)`, args...).Scan(&st.Views, &st.Visitors)
	if err != nil {
		return st, err
	}
	err = s.r.QueryRow(`select count(*), coalesce(sum(n = 1), 0), coalesce(sum(dur), 0) from (
			select count(*) n, max(e.ts) - min(e.ts) dur
			from events e where `+w+` and e.kind in (1, 2) group by e.visit)`, args...).
		Scan(&st.Visits, &st.Bounces, &st.Duration)
	return st, err
}

// Point is one bucket of a time series.
type Point struct {
	Day      int // yyyymmdd
	Hour     int // 0-23, or -1 for daily buckets
	Views    int64
	Visitors int64
}

// Series returns views and visitors per day, or per hour when hourly is set.
// Buckets without events are absent; the caller fills the gaps.
func (s *Store) Series(q Query, hourly bool) ([]Point, error) {
	w, args := q.where()
	hourCol, group := "-1", "e.local_day"
	if hourly {
		hourCol, group = "e.local_hour", "e.local_day, e.local_hour"
	}
	rows, err := s.r.Query(`select e.local_day, `+hourCol+`, coalesce(sum(e.kind = 1), 0), count(distinct e.visitor)
		from events e where `+w+` and e.kind in (1, 2) group by `+group+` order by 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.Day, &p.Hour, &p.Views, &p.Visitors); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Row is one line of a breakdown.
type Row struct {
	Value    string
	Visitors int64
	Count    int64 // page views, or events for the event breakdown
}

// Breakdown lists the top values of a dimension by visitors. The dimension
// "entry" lists the first page of each visit.
func (s *Store) Breakdown(q Query, dim string, limit int) ([]Row, error) {
	w, args := q.where()
	var query string
	if dim == "entry" {
		// With a bare column next to min(), SQLite takes it from the row
		// holding the minimum, i.e. the first page view of the visit.
		query = `select coalesce(path, ''), count(distinct visitor), count(*) from (
				select e.visit, e.visitor, e.path, min(e.id) from events e
				where ` + w + ` and e.kind = 1 group by e.visit)
			group by 1 order by 2 desc, 3 desc, 1 limit ?`
	} else {
		d, ok := dimensions[dim]
		if !ok {
			return nil, fmt.Errorf("unknown dimension %q", dim)
		}
		query = `select coalesce(e.` + d.col + `, ''), count(distinct e.visitor), count(*)
			from events e where ` + w + ` and ` + d.cond + `
			group by 1 order by 2 desc, 3 desc, 1 limit ?`
	}
	rows, err := s.r.Query(query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Value, &r.Visitors, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PropValue is one observed value of an event property.
type PropValue struct {
	Value    string
	Count    int64
	Visitors int64
}

// Prop summarises one property of a custom event.
type Prop struct {
	Key     string
	Count   int64
	Numeric bool // every value seen in the period was a number
	Sum     float64
	Avg     float64
	Min     float64
	Max     float64
	Values  []PropValue
}

// EventProps summarises the properties sent with the named custom event:
// the most common values of each key and, for numbers, their totals.
func (s *Store) EventProps(q Query, event string, valuesPerKey int) ([]Prop, error) {
	w, args := q.where()
	args = append(args, event)
	const numeric = `je.type in ('integer', 'real')`

	rows, err := s.r.Query(`select je.key, count(*), coalesce(sum(`+numeric+`), 0),
			coalesce(sum(case when `+numeric+` then je.value end), 0),
			coalesce(min(case when `+numeric+` then je.value end), 0),
			coalesce(max(case when `+numeric+` then je.value end), 0)
		from events e, json_each(e.props) je
		where `+w+` and e.kind = 2 and e.name = ? and e.props is not null
		group by je.key order by 2 desc, 1`, args...)
	if err != nil {
		return nil, err
	}
	var props []Prop
	index := map[string]int{}
	for rows.Next() {
		var p Prop
		var numericN int64
		if err := rows.Scan(&p.Key, &p.Count, &numericN, &p.Sum, &p.Min, &p.Max); err != nil {
			rows.Close()
			return nil, err
		}
		if numericN > 0 {
			p.Numeric = numericN == p.Count
			p.Avg = p.Sum / float64(numericN)
		}
		index[p.Key] = len(props)
		props = append(props, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.r.Query(`select je.key, case je.type when 'true' then 'true' when 'false' then 'false'
				else cast(je.value as text) end, count(*), count(distinct e.visitor)
		from events e, json_each(e.props) je
		where `+w+` and e.kind = 2 and e.name = ? and e.props is not null
		group by 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var v PropValue
		if err := rows.Scan(&key, &v.Value, &v.Count, &v.Visitors); err != nil {
			return nil, err
		}
		if i, ok := index[key]; ok {
			props[i].Values = append(props[i].Values, v)
		}
	}
	for i := range props {
		vals := props[i].Values
		sort.Slice(vals, func(a, b int) bool {
			if vals[a].Count != vals[b].Count {
				return vals[a].Count > vals[b].Count
			}
			return vals[a].Value < vals[b].Value
		})
		if len(vals) > valuesPerKey {
			props[i].Values = vals[:valuesPerKey]
		}
	}
	return props, rows.Err()
}

// Recent is one line of the realtime feed.
type Recent struct {
	TS      int64
	Kind    int
	Name    string
	Path    string
	Country string
	Browser string
	RefHost string
}

// Realtime describes the last few minutes of activity.
type Realtime struct {
	Active  int64           // distinct visitors in the last 5 minutes
	Minutes map[int64]int64 // page views and events per unix minute
	Recent  []Recent
}

// Realtime reads the half hour before now.
func (s *Store) Realtime(siteID, now int64) (*Realtime, error) {
	rt := &Realtime{Minutes: map[int64]int64{}}
	err := s.r.QueryRow(`select count(distinct visitor) from events
		where site_id = ? and ts >= ? and kind in (1, 2)`, siteID, now-300).Scan(&rt.Active)
	if err != nil {
		return nil, err
	}
	rows, err := s.r.Query(`select ts / 60, count(*) from events
		where site_id = ? and ts >= ? and kind in (1, 2) group by 1`, siteID, now-1800)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m, n int64
		if err := rows.Scan(&m, &n); err != nil {
			rows.Close()
			return nil, err
		}
		rt.Minutes[m] = n
	}
	rows.Close()
	rows, err = s.r.Query(`select ts, kind, coalesce(name, ''), coalesce(path, ''), coalesce(country, ''),
			coalesce(browser, ''), coalesce(ref_host, '')
		from events where site_id = ? and ts >= ? and kind in (1, 2) order by ts desc, id desc limit 25`, siteID, now-1800)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Recent
		if err := rows.Scan(&r.TS, &r.Kind, &r.Name, &r.Path, &r.Country, &r.Browser, &r.RefHost); err != nil {
			return nil, err
		}
		rt.Recent = append(rt.Recent, r)
	}
	return rt, rows.Err()
}

// DailyViews returns a site's page views per local day since the given
// time, for the sparklines on the overview page.
func (s *Store) DailyViews(siteID, since int64) (map[int]int64, error) {
	rows, err := s.r.Query(`select local_day, count(*) from events
		where site_id = ? and ts >= ? and kind = 1 group by 1`, siteID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int64{}
	for rows.Next() {
		var day int
		var n int64
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day] = n
	}
	return out, rows.Err()
}

// CountEvents returns the total number of stored events, for diagnostics.
func (s *Store) CountEvents() (int64, error) {
	var n int64
	err := s.r.QueryRow(`select count(*) from events`).Scan(&n)
	return n, err
}

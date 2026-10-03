package store

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Event kinds.
const (
	KindPageview    = 1
	KindCustom      = 2
	KindPerformance = 3
	KindIdentify    = 4
)

// Event is one row of the events table. It never carries an IP address or a
// raw user agent: by the time an Event exists, those have been reduced to a
// salted visitor hash and coarse descriptors.
type Event struct {
	SiteID    int64
	TS        int64
	LocalDay  int // yyyymmdd in the site's timezone
	LocalHour int
	Kind      int
	Name      string
	Visitor   []byte
	Visit     []byte

	Host, Path, Title         string
	RefHost, RefPath, Channel string
	UTMSource, UTMMedium      string
	UTMCampaign, UTMContent   string
	UTMTerm                   string
	Browser, OS, Device       string
	Screen, Lang              string
	Country, Region, City     string
	Tag, DistinctID           string
	LCP, INP, CLS, FCP, TTFB  *float64
	Props                     map[string]any
}

// ErrQueueFull is returned when events arrive faster than they can be written.
var ErrQueueFull = errors.New("event queue is full")

const (
	queueSize     = 8192
	batchSize     = 200
	flushInterval = time.Second
)

type writer struct {
	s     *Store
	ch    chan *Event
	flush chan chan struct{}
	done  chan struct{}
	once  sync.Once

	// seenKeys avoids rewriting the property catalogue for every event.
	seenKeys map[propKey]seenProp
}

type propKey struct {
	site       int64
	event, key string
}

type seenProp struct {
	typ string
	day int
}

// StartWriter begins the background goroutine that batches event inserts.
func (s *Store) StartWriter() {
	w := &writer{
		s:        s,
		ch:       make(chan *Event, queueSize),
		flush:    make(chan chan struct{}),
		done:     make(chan struct{}),
		seenKeys: make(map[propKey]seenProp),
	}
	s.writer = w
	go w.run()
}

// Enqueue hands an event to the writer. It does not block: if the queue is
// full the event is refused so the caller can tell the client to back off.
func (s *Store) Enqueue(e *Event) error {
	select {
	case s.writer.ch <- e:
		return nil
	default:
		return ErrQueueFull
	}
}

// Flush blocks until everything queued so far has been written.
func (s *Store) Flush() {
	ack := make(chan struct{})
	s.writer.flush <- ack
	<-ack
}

func (w *writer) stop() {
	w.once.Do(func() {
		close(w.ch)
		<-w.done
	})
}

func (w *writer) run() {
	defer close(w.done)
	tick := time.NewTicker(flushInterval)
	defer tick.Stop()
	batch := make([]*Event, 0, batchSize)
	write := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.insert(batch); err != nil {
			slog.Error("writing events failed", "events", len(batch), "err", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case e, ok := <-w.ch:
			if !ok {
				write()
				return
			}
			batch = append(batch, e)
			if len(batch) >= batchSize {
				write()
			}
		case <-tick.C:
			write()
		case ack := <-w.flush:
			// Drain whatever is already queued, then write it all.
			for drained := false; !drained; {
				select {
				case e, ok := <-w.ch:
					if !ok {
						drained = true
						break
					}
					batch = append(batch, e)
					if len(batch) >= batchSize {
						write()
					}
				default:
					drained = true
				}
			}
			write()
			close(ack)
		}
	}
}

func nz(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nf(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

const insertEvent = `insert into events (
	site_id, ts, local_day, local_hour, kind, name, visitor, visit,
	host, path, title, ref_host, ref_path, channel,
	utm_source, utm_medium, utm_campaign, utm_content, utm_term,
	browser, os, device, screen, lang, country, region, city,
	tag, distinct_id, lcp, inp, cls, fcp, ttfb, props
) values (?,?,?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?)`

func (w *writer) insert(batch []*Event) error {
	tx, err := w.s.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(insertEvent)
	if err != nil {
		return err
	}
	defer stmt.Close()

	type newKey struct {
		propKey
		seenProp
		ts int64
	}
	var keys []newKey

	for _, e := range batch {
		var props any
		if len(e.Props) > 0 {
			b, err := json.Marshal(e.Props)
			if err != nil {
				return err
			}
			props = string(b)
			for k, v := range e.Props {
				pk := propKey{e.SiteID, e.Name, k}
				sp := seenProp{propType(v), e.LocalDay}
				if w.seenKeys[pk] != sp {
					w.seenKeys[pk] = sp
					keys = append(keys, newKey{pk, sp, e.TS})
				}
			}
		}
		if _, err := stmt.Exec(
			e.SiteID, e.TS, e.LocalDay, e.LocalHour, e.Kind, nz(e.Name), e.Visitor, e.Visit,
			nz(e.Host), nz(e.Path), nz(e.Title), nz(e.RefHost), nz(e.RefPath), nz(e.Channel),
			nz(e.UTMSource), nz(e.UTMMedium), nz(e.UTMCampaign), nz(e.UTMContent), nz(e.UTMTerm),
			nz(e.Browser), nz(e.OS), nz(e.Device), nz(e.Screen), nz(e.Lang), nz(e.Country), nz(e.Region), nz(e.City),
			nz(e.Tag), nz(e.DistinctID), nf(e.LCP), nf(e.INP), nf(e.CLS), nf(e.FCP), nf(e.TTFB), props,
		); err != nil {
			return err
		}
	}
	for _, k := range keys {
		if _, err := tx.Exec(`insert into property_keys (site_id, event, key, type, last_seen) values (?, ?, ?, ?, ?)
			on conflict (site_id, event, key) do update set type = excluded.type, last_seen = excluded.last_seen`,
			k.site, k.event, k.key, k.typ, k.ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func propType(v any) string {
	switch v.(type) {
	case float64, json.Number, int, int64:
		return "number"
	case bool:
		return "boolean"
	default:
		return "string"
	}
}

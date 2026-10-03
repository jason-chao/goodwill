// Package geo looks up the country (and, with a city database, the region
// and city) of an address. It reads MaxMind DB (.mmdb) files, the format
// used by both DB-IP and MaxMind databases.
package geo

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/netip"
	"os"

	"github.com/oschwald/maxminddb-golang/v2"
)

// DB is an open location database. A nil *DB is valid and finds nothing.
type DB struct {
	r      *maxminddb.Reader
	Source string // where the data came from, for the startup log
}

// Location is the result of a lookup. Fields are empty when unknown.
type Location struct {
	Country string // ISO 3166-1 alpha-2
	Region  string // ISO 3166-2, e.g. "GB-ENG"
	City    string
}

// Open loads the database at path. With an empty path it uses the country
// database built into the binary, if there is one, and otherwise returns nil.
func Open(path string) (*DB, error) {
	if path != "" {
		r, err := maxminddb.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open location database %s: %w", path, err)
		}
		return &DB{r: r, Source: path}, nil
	}
	if len(embedded) == 0 {
		return nil, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(embedded))
	if err != nil {
		return nil, err
	}
	const source = "built-in country database"

	// Unpack to a temporary file and map it, rather than holding the whole
	// database on the heap: mapped pages are only loaded when a lookup
	// touches them, and the kernel can drop them again under pressure.
	if f, err := os.CreateTemp("", "goodwill-geo-*.mmdb"); err == nil {
		_, copyErr := io.Copy(f, zr)
		closeErr := f.Close()
		if copyErr == nil && closeErr == nil {
			r, err := maxminddb.Open(f.Name())
			// The mapping outlives the name, so nothing is left behind.
			os.Remove(f.Name())
			if err == nil {
				return &DB{r: r, Source: source}, nil
			}
		}
		os.Remove(f.Name())
		if zr, err = gzip.NewReader(bytes.NewReader(embedded)); err != nil {
			return nil, err
		}
	}

	// No writable temporary directory: fall back to memory.
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	r, err := maxminddb.OpenBytes(raw)
	if err != nil {
		return nil, err
	}
	return &DB{r: r, Source: source}, nil
}

// Embedded reports whether a country database is built into the binary.
func Embedded() bool { return len(embedded) > 0 }

func (db *DB) Close() error {
	if db == nil {
		return nil
	}
	return db.r.Close()
}

type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

// Lookup returns what the database knows about addr.
func (db *DB) Lookup(addr netip.Addr) Location {
	if db == nil || !addr.IsValid() {
		return Location{}
	}
	var rec record
	if err := db.r.Lookup(addr).Decode(&rec); err != nil {
		return Location{}
	}
	loc := Location{Country: rec.Country.ISOCode, City: rec.City.Names["en"]}
	if len(rec.Subdivisions) > 0 && rec.Subdivisions[0].ISOCode != "" && loc.Country != "" {
		loc.Region = loc.Country + "-" + rec.Subdivisions[0].ISOCode
	}
	return loc
}

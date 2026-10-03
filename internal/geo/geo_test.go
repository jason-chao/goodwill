package geo

import (
	"net/netip"
	"testing"
)

func TestNilDatabase(t *testing.T) {
	var db *DB
	if got := db.Lookup(netip.MustParseAddr("192.0.2.1")); got != (Location{}) {
		t.Errorf("nil database returned %+v", got)
	}
	if err := db.Close(); err != nil {
		t.Error(err)
	}
	if _, err := Open("/nonexistent/file.mmdb"); err == nil {
		t.Error("a missing file should be an error")
	}
}

func TestEmbeddedCountryDatabase(t *testing.T) {
	if !Embedded() {
		t.Skip("built without the country database (no embedgeo tag)")
	}
	db, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Addresses reserved for documentation belong to no country.
	if got := db.Lookup(netip.MustParseAddr("192.0.2.1")); got.Country != "" {
		t.Errorf("documentation address resolved to %q", got.Country)
	}
	// A root name server operated from the United States.
	if got := db.Lookup(netip.MustParseAddr("198.41.0.4")); got.Country != "US" {
		t.Errorf("198.41.0.4 resolved to %q, want US", got.Country)
	}
	if got := db.Lookup(netip.Addr{}); got != (Location{}) {
		t.Errorf("invalid address resolved to %+v", got)
	}
}

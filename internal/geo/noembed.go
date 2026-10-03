//go:build !embedgeo

package geo

// Built without a country database. Point geo.path at an .mmdb file to
// enable location lookups, or build with "make build".
var embedded []byte

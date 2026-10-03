//go:build embedgeo

package geo

import _ "embed"

// The country database is downloaded by "make geo" and is not kept in the
// repository. See NOTICE for its attribution.
//
//go:embed data/country.mmdb.gz
var embedded []byte

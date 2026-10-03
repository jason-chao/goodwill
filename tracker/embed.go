// Package tracker holds the browser tracking script.
//
// script.js is the Umami tracker, built unmodified from the release named
// in VERSION by scripts/build-tracker.sh. It is distributed under the MIT
// licence in LICENSE.
package tracker

import _ "embed"

//go:embed script.js
var Script []byte

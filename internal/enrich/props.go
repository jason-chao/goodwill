package enrich

import (
	"encoding/json"
	"math"
	"sort"
)

// Limits on event properties.
const (
	MaxProps        = 30
	MaxPropKeyLen   = 64
	MaxPropValueLen = 255
)

// Props cleans the free-form data sent with an event.
//
// Nested objects are flattened to dotted keys. Numbers and booleans keep
// their type; long strings are shortened. A property that cannot be stored
// (a key that is too long, an oversized array) is dropped on its own, and
// anything past the first MaxProps keys is dropped too. The rest of the
// event is always kept.
func Props(data map[string]any) map[string]any {
	if len(data) == 0 {
		return nil
	}
	flat := map[string]any{}
	flatten("", data, flat, 0)
	if len(flat) == 0 {
		return nil
	}
	if len(flat) > MaxProps {
		keys := make([]string, 0, len(flat))
		for k := range flat {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[MaxProps:] {
			delete(flat, k)
		}
	}
	return flat
}

func flatten(prefix string, in map[string]any, out map[string]any, depth int) {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if key == "" || len(key) > MaxPropKeyLen {
			continue
		}
		switch t := v.(type) {
		case nil:
		case map[string]any:
			if depth < 8 {
				flatten(key, t, out, depth+1)
			}
		case string:
			if t != "" {
				out[key] = Truncate(t, MaxPropValueLen)
			}
		case float64:
			if !math.IsNaN(t) && !math.IsInf(t, 0) {
				out[key] = t
			}
		case bool:
			out[key] = t
		case []any:
			// Arrays are kept as their JSON text when short enough.
			if b, err := json.Marshal(t); err == nil && len(b) <= MaxPropValueLen {
				out[key] = string(b)
			}
		}
	}
}

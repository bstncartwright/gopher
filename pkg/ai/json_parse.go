package ai

import (
	"encoding/json"
	"strings"
)

// ParseStreamingJSON tries to parse possibly-incomplete JSON and always returns an object map.
func ParseStreamingJSON(partial string) map[string]any {
	partial = strings.TrimSpace(partial)
	if partial == "" {
		return map[string]any{}
	}

	if obj, ok := parseJSONObject(partial); ok {
		return obj
	}

	// Scan backwards for structural JSON closing characters instead of trying every position.
	for i := len(partial) - 1; i > 0; i-- {
		ch := partial[i]
		if ch == '}' || ch == ']' || ch == '"' || ch == 't' || ch == 'e' || ch == 'l' || (ch >= '0' && ch <= '9') {
			if obj, ok := parseJSONObject(partial[:i+1]); ok {
				return obj
			}
		}
	}

	return map[string]any{}
}

func parseJSONObject(raw string) (map[string]any, bool) {
	var anyValue any
	if err := json.Unmarshal([]byte(raw), &anyValue); err != nil {
		return nil, false
	}
	obj, ok := anyValue.(map[string]any)
	if !ok {
		return nil, false
	}
	return obj, true
}

package ai

import "strings"

// SanitizeSurrogates removes surrogate code points that can break downstream JSON encoders.
func SanitizeSurrogates(text string) string {
	if text == "" {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

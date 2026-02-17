package memory

import (
	"sort"
	"strings"
	"unicode"
)

var defaultStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {}, "for": {},
	"from": {}, "how": {}, "in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {}, "that": {},
	"the": {}, "this": {}, "to": {}, "was": {}, "were": {}, "what": {}, "when": {}, "where": {},
	"which": {}, "who": {}, "will": {}, "with": {}, "you": {}, "your": {},
}

func NormalizeKeywords(words []string) []string {
	if len(words) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(words))
	out := make([]string, 0, len(words))
	for _, word := range words {
		for _, token := range tokenize(word) {
			if _, stop := defaultStopWords[token]; stop {
				continue
			}
			if _, exists := seen[token]; exists {
				continue
			}
			seen[token] = struct{}{}
			out = append(out, token)
		}
	}
	sort.Strings(out)
	return out
}

func ExtractKeywords(text string, max int) []string {
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return nil
	}

	counts := map[string]int{}
	for _, token := range tokens {
		if _, stop := defaultStopWords[token]; stop {
			continue
		}
		counts[token]++
	}
	if len(counts) == 0 {
		return nil
	}

	type tokenCount struct {
		token string
		count int
	}
	ordered := make([]tokenCount, 0, len(counts))
	for token, count := range counts {
		ordered = append(ordered, tokenCount{token: token, count: count})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].count != ordered[j].count {
			return ordered[i].count > ordered[j].count
		}
		return ordered[i].token < ordered[j].token
	})

	if max <= 0 || max > len(ordered) {
		max = len(ordered)
	}
	out := make([]string, 0, max)
	for i := 0; i < max; i++ {
		out = append(out, ordered[i].token)
	}
	sort.Strings(out)
	return out
}

func tokenize(text string) []string {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if trimmed == "" {
		return nil
	}

	runes := []rune(trimmed)
	out := make([]string, 0, 16)
	start := -1
	for i, r := range runes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, string(runes[start:i]))
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, string(runes[start:]))
	}
	return out
}

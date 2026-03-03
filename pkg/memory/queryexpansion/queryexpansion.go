package queryexpansion

import (
	"sort"
	"strings"
	"unicode"
)

func Expand(query string, maxTerms int) []string {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil
	}
	if maxTerms <= 0 {
		maxTerms = 16
	}
	counts := make(map[string]int, len(tokens))
	for _, token := range tokens {
		if _, stop := stopWords[token]; stop {
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
	if maxTerms > len(ordered) {
		maxTerms = len(ordered)
	}
	out := make([]string, 0, maxTerms)
	for i := 0; i < maxTerms; i++ {
		out = append(out, ordered[i].token)
	}
	sort.Strings(out)
	return out
}

func ExpandWithOriginal(query string, maxTerms int) []string {
	terms := Expand(query, maxTerms)
	trimmed := strings.TrimSpace(strings.ToLower(query))
	if trimmed == "" {
		return terms
	}
	if len(terms) == 0 {
		return []string{trimmed}
	}
	seen := make(map[string]struct{}, len(terms)+1)
	out := make([]string, 0, len(terms)+1)
	for _, term := range terms {
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	if _, ok := seen[trimmed]; !ok {
		out = append(out, trimmed)
	}
	return out
}

func BuildFTSQuery(query string) string {
	terms := Expand(query, 24)
	if len(terms) == 0 {
		return strings.TrimSpace(query)
	}
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		if term == "" {
			continue
		}
		parts = append(parts, quoteToken(term))
	}
	return strings.Join(parts, " OR ")
}

func quoteToken(token string) string {
	token = strings.ReplaceAll(token, `"`, "")
	return `"` + token + `"`
}

func tokenize(text string) []string {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if trimmed == "" {
		return nil
	}
	runes := []rune(trimmed)
	out := make([]string, 0, 32)
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

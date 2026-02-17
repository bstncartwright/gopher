package retrieval

import (
	"math"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/memory"
)

type ScoreWeights struct {
	Recency         float64
	Similarity      float64
	Keywords        float64
	Importance      float64
	SessionAffinity float64
}

func DefaultScoreWeights() ScoreWeights {
	return ScoreWeights{
		Recency:         0.26,
		Similarity:      0.34,
		Keywords:        0.16,
		Importance:      0.12,
		SessionAffinity: 0.12,
	}
}

type ScoreInput struct {
	Record     memory.MemoryRecord
	Query      memory.MemoryQuery
	Similarity float64
	Now        time.Time
}

func Score(input ScoreInput, weights ScoreWeights) float64 {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	recencyScore := recency(input.Record.Timestamp, now)
	similarityScore := normalizeSimilarity(input.Similarity)
	keywordScore := keywordMatch(input.Record.Content, input.Query.Keywords)
	importance := clamp01(input.Record.Importance)
	affinity := sessionAffinity(input.Record, input.Query)

	total :=
		recencyScore*weights.Recency +
			similarityScore*weights.Similarity +
			keywordScore*weights.Keywords +
			importance*weights.Importance +
			affinity*weights.SessionAffinity

	// Add a tiny deterministic tiebreaker so sorting remains stable across equal scores.
	if !input.Record.Timestamp.IsZero() {
		total += float64(input.Record.Timestamp.Unix()%1000) * 1e-9
	}
	return total
}

func recency(ts, now time.Time) float64 {
	if ts.IsZero() {
		return 0
	}
	ageHours := now.Sub(ts.UTC()).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	// Half-life style decay with day-scale memory.
	return 1 / (1 + (ageHours / 24.0))
}

func normalizeSimilarity(similarity float64) float64 {
	if math.IsNaN(similarity) || math.IsInf(similarity, 0) {
		return 0
	}
	if similarity < -1 {
		similarity = -1
	}
	if similarity > 1 {
		similarity = 1
	}
	return (similarity + 1) / 2
}

func keywordMatch(content string, keywords []string) float64 {
	if len(keywords) == 0 {
		return 0
	}
	lower := strings.ToLower(content)
	matches := 0
	for _, keyword := range keywords {
		if keyword == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(keyword)) {
			matches++
		}
	}
	return float64(matches) / float64(len(keywords))
}

func sessionAffinity(record memory.MemoryRecord, query memory.MemoryQuery) float64 {
	if query.SessionID != "" && record.SessionID == query.SessionID {
		return 1
	}
	if query.AgentID != "" && record.AgentID == query.AgentID {
		return 0.7
	}
	if query.AgentID != "" && record.Scope == memory.AgentScope(query.AgentID) {
		return 0.6
	}
	if record.Scope == memory.ScopeGlobal {
		return 0.3
	}
	return 0
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

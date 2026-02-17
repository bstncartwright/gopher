package memory

import (
	"sort"
	"strings"
	"time"
)

type MemoryType int

const (
	MemoryEpisodic MemoryType = iota
	MemorySemantic
	MemoryProcedural
	MemoryTool
)

func (t MemoryType) String() string {
	switch t {
	case MemoryEpisodic:
		return "episodic"
	case MemorySemantic:
		return "semantic"
	case MemoryProcedural:
		return "procedural"
	case MemoryTool:
		return "tool"
	default:
		return "unknown"
	}
}

type MemoryScope string

const ScopeGlobal MemoryScope = "global"

func ProjectScope(projectID string) MemoryScope {
	id := strings.TrimSpace(projectID)
	if id == "" {
		return ScopeGlobal
	}
	return MemoryScope("project:" + id)
}

func AgentScope(agentID string) MemoryScope {
	id := strings.TrimSpace(agentID)
	if id == "" {
		return ScopeGlobal
	}
	return MemoryScope("agent:" + id)
}

func SessionScope(sessionID string) MemoryScope {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return ScopeGlobal
	}
	return MemoryScope("session:" + id)
}

type TimeRange struct {
	Start time.Time
	End   time.Time
}

func (r *TimeRange) Contains(ts time.Time) bool {
	if r == nil {
		return true
	}
	if ts.IsZero() {
		return false
	}
	at := ts.UTC()
	if !r.Start.IsZero() && at.Before(r.Start.UTC()) {
		return false
	}
	if !r.End.IsZero() && at.After(r.End.UTC()) {
		return false
	}
	return true
}

type MemoryRecord struct {
	ID         string
	Type       MemoryType
	Scope      MemoryScope
	SessionID  string
	AgentID    string
	Content    string
	Metadata   map[string]string
	Embedding  []float32
	Importance float64
	Timestamp  time.Time
}

type MemoryQuery struct {
	SessionID      string
	AgentID        string
	Topic          string
	Keywords       []string
	Limit          int
	TimeRange      *TimeRange
	Types          []MemoryType
	Scopes         []MemoryScope
	QueryEmbedding []float32
}

const (
	DefaultRetrieveLimit = 8
	MaxRetrieveLimit     = 128
)

func NormalizeRecord(record MemoryRecord, now time.Time) MemoryRecord {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = now.UTC()
	} else {
		record.Timestamp = record.Timestamp.UTC()
	}
	record.Content = strings.TrimSpace(record.Content)
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.AgentID = strings.TrimSpace(record.AgentID)
	record.ID = strings.TrimSpace(record.ID)
	if strings.TrimSpace(string(record.Scope)) == "" {
		record.Scope = ScopeGlobal
	} else {
		record.Scope = MemoryScope(strings.TrimSpace(string(record.Scope)))
	}
	record.Metadata = cloneStringMap(record.Metadata)
	record.Embedding = cloneEmbedding(record.Embedding)
	record.Importance = clamp01(record.Importance)
	return record
}

func NormalizeQuery(query MemoryQuery) MemoryQuery {
	query.SessionID = strings.TrimSpace(query.SessionID)
	query.AgentID = strings.TrimSpace(query.AgentID)
	query.Topic = strings.TrimSpace(query.Topic)
	if query.Limit <= 0 {
		query.Limit = DefaultRetrieveLimit
	}
	if query.Limit > MaxRetrieveLimit {
		query.Limit = MaxRetrieveLimit
	}
	query.Keywords = NormalizeKeywords(query.Keywords)

	if len(query.Types) > 0 {
		typeSet := make(map[MemoryType]struct{}, len(query.Types))
		normalizedTypes := make([]MemoryType, 0, len(query.Types))
		for _, t := range query.Types {
			if _, exists := typeSet[t]; exists {
				continue
			}
			typeSet[t] = struct{}{}
			normalizedTypes = append(normalizedTypes, t)
		}
		sort.SliceStable(normalizedTypes, func(i, j int) bool {
			return normalizedTypes[i] < normalizedTypes[j]
		})
		query.Types = normalizedTypes
	}

	if len(query.Scopes) > 0 {
		scopeSet := make(map[MemoryScope]struct{}, len(query.Scopes))
		normalizedScopes := make([]MemoryScope, 0, len(query.Scopes))
		for _, scope := range query.Scopes {
			clean := MemoryScope(strings.TrimSpace(string(scope)))
			if clean == "" {
				continue
			}
			if _, exists := scopeSet[clean]; exists {
				continue
			}
			scopeSet[clean] = struct{}{}
			normalizedScopes = append(normalizedScopes, clean)
		}
		sort.SliceStable(normalizedScopes, func(i, j int) bool {
			return normalizedScopes[i] < normalizedScopes[j]
		})
		query.Scopes = normalizedScopes
	}

	if query.TimeRange != nil {
		rangeCopy := *query.TimeRange
		if !rangeCopy.Start.IsZero() {
			rangeCopy.Start = rangeCopy.Start.UTC()
		}
		if !rangeCopy.End.IsZero() {
			rangeCopy.End = rangeCopy.End.UTC()
		}
		query.TimeRange = &rangeCopy
	}

	query.QueryEmbedding = cloneEmbedding(query.QueryEmbedding)
	return query
}

func cloneEmbedding(in []float32) []float32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float32, len(in))
	copy(out, in)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
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

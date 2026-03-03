package memory

import (
	"context"
	"strings"
	"time"
)

type MemorySearchRequest struct {
	Query      string
	MaxResults int
	MinScore   float64
	SessionKey string
}

type MemorySearchResult struct {
	ID        string
	Path      string
	StartLine int
	EndLine   int
	Score     float64
	Snippet   string
	Source    string
	Citation  string
	Metadata  map[string]string
}

type MemorySearchResponse struct {
	Results           []MemorySearchResult
	Mode              string
	Provider          string
	Model             string
	Disabled          bool
	Unavailable       bool
	Warning           string
	Action            string
	Error             string
	FallbackReason    string
	UnavailableReason string
}

type MemoryReadRequest struct {
	Path  string
	From  int
	Lines int
}

type MemoryReadResponse struct {
	Path      string
	StartLine int
	EndLine   int
	Text      string
}

type MemorySearchStatus struct {
	Enabled            bool
	Mode               string
	Provider           string
	Model              string
	Files              int
	Chunks             int
	FTSAvailable       bool
	VectorAvailable    bool
	Dirty              bool
	LastSync           time.Time
	FallbackReason     string
	UnavailableReason  string
	ProviderError      string
	EmbeddingAvailable bool
}

func (s MemorySearchStatus) RetrievalMode() string {
	mode := strings.TrimSpace(strings.ToLower(s.Mode))
	if mode == "" {
		if s.VectorAvailable && s.FTSAvailable {
			return "hybrid"
		}
		if s.FTSAvailable {
			return "fts-only"
		}
		if s.VectorAvailable {
			return "vector-only"
		}
		return "unavailable"
	}
	return mode
}

type MemorySearchManager interface {
	Search(ctx context.Context, req MemorySearchRequest) (MemorySearchResponse, error)
	Read(ctx context.Context, req MemoryReadRequest) (MemoryReadResponse, error)
	Status(ctx context.Context) (MemorySearchStatus, error)
	Sync(ctx context.Context, force bool) error
	ProbeEmbedding(ctx context.Context) error
}

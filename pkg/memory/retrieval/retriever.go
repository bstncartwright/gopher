package retrieval

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bstncartwright/gopher/pkg/memory"
)

type HybridRetrieverOptions struct {
	Weights             ScoreWeights
	CandidateMultiplier int
	Now                 func() time.Time
}

type HybridRetriever struct {
	weights             ScoreWeights
	candidateMultiplier int
	now                 func() time.Time
}

var _ memory.Retriever = (*HybridRetriever)(nil)

func NewHybridRetriever(opts HybridRetrieverOptions) *HybridRetriever {
	weights := opts.Weights
	if weights == (ScoreWeights{}) {
		weights = DefaultScoreWeights()
	}
	candidateMultiplier := opts.CandidateMultiplier
	if candidateMultiplier <= 0 {
		candidateMultiplier = 8
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &HybridRetriever{
		weights:             weights,
		candidateMultiplier: candidateMultiplier,
		now:                 nowFn,
	}
}

func (r *HybridRetriever) Retrieve(ctx context.Context, store memory.CandidateStore, query memory.MemoryQuery) ([]memory.MemoryRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return nil, fmt.Errorf("candidate store is required")
	}

	query = memory.NormalizeQuery(query)
	candidateQuery := query
	candidateQuery.Limit = query.Limit * r.candidateMultiplier
	if candidateQuery.Limit < 32 {
		candidateQuery.Limit = 32
	}
	if candidateQuery.Limit > 1024 {
		candidateQuery.Limit = 1024
	}

	candidates, err := store.List(ctx, candidateQuery)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	scored := make([]scoredRecord, 0, len(candidates))
	now := r.now().UTC()
	for _, candidate := range candidates {
		similarity := 0.0
		if len(query.QueryEmbedding) > 0 && len(candidate.Embedding) > 0 {
			sim, err := memory.CosineSimilarity(query.QueryEmbedding, candidate.Embedding)
			if err == nil {
				similarity = sim
			}
		}
		score := Score(ScoreInput{
			Record:     candidate,
			Query:      query,
			Similarity: similarity,
			Now:        now,
		}, r.weights)
		scored = append(scored, scoredRecord{record: candidate, score: score})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if !scored[i].record.Timestamp.Equal(scored[j].record.Timestamp) {
			return scored[i].record.Timestamp.After(scored[j].record.Timestamp)
		}
		return scored[i].record.ID < scored[j].record.ID
	})

	limit := query.Limit
	if limit <= 0 || limit > len(scored) {
		limit = len(scored)
	}
	out := make([]memory.MemoryRecord, 0, limit)
	for i := 0; i < limit; i++ {
		record := scored[i].record
		record.Metadata = cloneMetadata(record.Metadata)
		record.Embedding = cloneEmbedding(record.Embedding)
		out = append(out, record)
	}
	return out, nil
}

type scoredRecord struct {
	record memory.MemoryRecord
	score  float64
}

func cloneMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneEmbedding(in []float32) []float32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float32, len(in))
	copy(out, in)
	return out
}

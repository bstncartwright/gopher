package memory

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
)

const defaultEmbeddingDimensions = 128

type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

type HashEmbedder struct {
	dimensions int
}

func NewHashEmbedder(dimensions int) *HashEmbedder {
	if dimensions <= 0 {
		dimensions = defaultEmbeddingDimensions
	}
	return &HashEmbedder{dimensions: dimensions}
}

func (e *HashEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	tokens := tokenize(text)
	if len(tokens) == 0 {
		return make([]float32, e.dimensions), nil
	}

	vector := make([]float32, e.dimensions)
	for _, token := range tokens {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		h1, h2 := dualHash(token)
		idx := int(h1 % uint64(e.dimensions))
		sign := float32(1)
		if h2%2 == 0 {
			sign = -1
		}
		vector[idx] += sign
	}
	normalizeL2(vector)
	return vector, nil
}

func dualHash(value string) (uint64, uint64) {
	h1 := fnv.New64a()
	_, _ = h1.Write([]byte(value))

	h2 := fnv.New64()
	_, _ = h2.Write([]byte(value))

	return h1.Sum64(), h2.Sum64()
}

func normalizeL2(values []float32) {
	if len(values) == 0 {
		return
	}
	var sumSquares float64
	for _, value := range values {
		sumSquares += float64(value * value)
	}
	if sumSquares == 0 {
		return
	}
	norm := float32(math.Sqrt(sumSquares))
	if norm == 0 {
		return
	}
	for i := range values {
		values[i] /= norm
	}
}

func CosineSimilarity(a, b []float32) (float64, error) {
	if len(a) == 0 || len(b) == 0 {
		return 0, nil
	}
	if len(a) != len(b) {
		return 0, fmt.Errorf("embedding dimensions do not match: %d != %d", len(a), len(b))
	}

	var dot float64
	var magA float64
	var magB float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		magA += av * av
		magB += bv * bv
	}
	if magA == 0 || magB == 0 {
		return 0, nil
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB)), nil
}

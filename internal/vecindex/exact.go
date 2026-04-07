package vecindex

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// ExactIndex is a deterministic pure-Go vector index for eval and harness use.
// It performs exact linear scans and avoids any cgo/native ANN dependency.
type ExactIndex struct {
	mu      sync.RWMutex
	dims    int
	vectors map[string][]float32
}

// NewExactIndex creates a pure-Go exact vector index with fixed dimensions.
func NewExactIndex(dims int) *ExactIndex {
	return &ExactIndex{
		dims:    dims,
		vectors: make(map[string][]float32),
	}
}

func (e *ExactIndex) Add(id string, vector []float32) error {
	if len(vector) != e.dims {
		return fmt.Errorf("vecindex: add %q: dimension mismatch", id)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vectors[id] = append([]float32(nil), vector...)
	return nil
}

func (e *ExactIndex) Search(vector []float32, k int) ([]ScoredID, error) {
	return e.SearchWithPrefix(vector, k, "")
}

func (e *ExactIndex) SearchWithPrefix(vector []float32, k int, prefix string) ([]ScoredID, error) {
	if len(vector) != e.dims {
		return nil, fmt.Errorf("vecindex: search: dimension mismatch")
	}
	if k <= 0 {
		return nil, nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]ScoredID, 0, len(e.vectors))
	for id, stored := range e.vectors {
		if prefix != "" && !strings.HasPrefix(id, prefix) {
			continue
		}
		sim := cosineSimilarity(vector, stored)
		out = append(out, ScoredID{
			ID:       id,
			Distance: 1 - sim,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance == out[j].Distance {
			return out[i].ID < out[j].ID
		}
		return out[i].Distance < out[j].Distance
	})
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func (e *ExactIndex) Remove(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.vectors, id)
	return nil
}

func (e *ExactIndex) Contains(id string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.vectors[id]
	return ok
}

func (e *ExactIndex) Len() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.vectors)
}

func (e *ExactIndex) Save() error {
	return nil
}

func (e *ExactIndex) Close() error {
	return nil
}

func (e *ExactIndex) ResetFromEmbeddings(m map[string][]float32) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vectors = make(map[string][]float32, len(m))
	for id, vector := range m {
		if len(vector) != e.dims {
			return fmt.Errorf("vecindex: reset %q: dimension mismatch", id)
		}
		e.vectors[id] = append([]float32(nil), vector...)
	}
	return nil
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		af := float64(a[i])
		bf := float64(b[i])
		dot += af * bf
		normA += af * af
		normB += bf * bf
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

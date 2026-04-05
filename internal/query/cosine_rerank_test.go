package query

import (
	"context"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func TestCosineReranker_ReordersBySimilarity(t *testing.T) {
	r := NewCosineReranker()
	r.SetQueryEmbedding([]float32{1, 0})

	items := []RankedItem{
		{Fact: model.Fact{ID: "a", Embedding: []float32{0, 1}}},
		{Fact: model.Fact{ID: "b", Embedding: []float32{1, 0}}},
	}
	out, err := r.Rerank(context.Background(), "q", items, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len %d", len(out))
	}
	if out[0].Fact.ID != "b" || out[1].Fact.ID != "a" {
		t.Fatalf("order want b,a got %s,%s", out[0].Fact.ID, out[1].Fact.ID)
	}
}

func TestCosineReranker_AppendsItemsWithoutEmbeddings(t *testing.T) {
	r := NewCosineReranker()
	r.SetQueryEmbedding([]float32{1, 0})

	items := []RankedItem{
		{Fact: model.Fact{ID: "a", Embedding: []float32{0, 1}}},
		{Fact: model.Fact{ID: "missing"}},
		{Fact: model.Fact{ID: "b", Embedding: []float32{1, 0}}},
	}
	out, err := r.Rerank(context.Background(), "q", items, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("len %d", len(out))
	}
	if out[0].Fact.ID != "b" || out[1].Fact.ID != "a" || out[2].Fact.ID != "missing" {
		t.Fatalf("unexpected order: %s,%s,%s", out[0].Fact.ID, out[1].Fact.ID, out[2].Fact.ID)
	}
}

func TestCosineReranker_NilQueryEmbeddingReturnsUnchanged(t *testing.T) {
	r := NewCosineReranker()
	items := []RankedItem{
		{Fact: model.Fact{ID: "a", Embedding: []float32{1, 0}}},
		{Fact: model.Fact{ID: "b", Embedding: []float32{0, 1}}},
	}
	out, err := r.Rerank(context.Background(), "q", items, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Fact.ID != "a" || out[1].Fact.ID != "b" {
		t.Fatalf("order changed with nil query embedding: %s,%s", out[0].Fact.ID, out[1].Fact.ID)
	}
}

func TestCosineReranker_EmptyItemsReturnsNil(t *testing.T) {
	r := NewCosineReranker()
	r.SetQueryEmbedding([]float32{1, 0})
	out, err := r.Rerank(context.Background(), "q", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("expected nil output, got %#v", out)
	}
}

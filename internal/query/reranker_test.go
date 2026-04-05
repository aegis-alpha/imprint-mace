package query

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

type mockReranker struct {
	fn func(ctx context.Context, query string, items []RankedItem, topN int) ([]RankedItem, error)
}

func (m *mockReranker) Rerank(ctx context.Context, query string, items []RankedItem, topN int) ([]RankedItem, error) {
	if m.fn != nil {
		return m.fn(ctx, query, items, topN)
	}
	return items, nil
}

func factPtr(id, content string) *model.Fact {
	f := model.Fact{ID: id, Content: content}
	return &f
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRerank_NilPassthrough(t *testing.T) {
	q := New(nil, nil, nil, "", discardLogger())
	a, b := factPtr("a", "one"), factPtr("b", "two")
	in := []rankedItem{{fact: a, score: 1}, {fact: b, score: 0.5}}
	out := q.applyRerankToRankedItems(context.Background(), "q", in)
	if len(out) != len(in) {
		t.Fatalf("len %d want %d", len(out), len(in))
	}
	if out[0].fact.ID != "a" || out[1].fact.ID != "b" {
		t.Fatalf("order changed with nil reranker: %#v", out)
	}
}

func TestRerank_MockReordersHead(t *testing.T) {
	rev := &mockReranker{fn: func(_ context.Context, _ string, items []RankedItem, _ int) ([]RankedItem, error) {
		out := make([]RankedItem, len(items))
		for i := range items {
			out[len(items)-1-i] = items[i]
			out[len(items)-1-i].Score = float64(i)
		}
		return out, nil
	}}
	q := New(nil, nil, nil, "", discardLogger(), WithReranker(rev, 2))
	a, b, c := factPtr("a", "a"), factPtr("b", "b"), factPtr("c", "c")
	in := []rankedItem{{fact: a, score: 1}, {fact: b, score: 0.9}, {fact: c, score: 0.8}}
	out := q.applyRerankToRankedItems(context.Background(), "question", in)
	if len(out) != 3 {
		t.Fatalf("len %d", len(out))
	}
	if out[0].fact.ID != "b" || out[1].fact.ID != "a" {
		t.Errorf("head reorder want b,a got %s,%s", out[0].fact.ID, out[1].fact.ID)
	}
	if out[2].fact.ID != "c" {
		t.Errorf("tail unchanged want c got %s", out[2].fact.ID)
	}
}

func TestRerank_OrderOnlyInvariant(t *testing.T) {
	shift := &mockReranker{fn: func(_ context.Context, _ string, items []RankedItem, _ int) ([]RankedItem, error) {
		if len(items) < 2 {
			return items, nil
		}
		out := make([]RankedItem, len(items))
		copy(out, items)
		out[0], out[1] = out[1], out[0]
		return out, nil
	}}
	q := New(nil, nil, nil, "", discardLogger(), WithReranker(shift, 0))
	items := make([]rankedItem, 5)
	ids := []string{"e0", "e1", "e2", "e3", "e4"}
	for i := range ids {
		items[i] = rankedItem{fact: factPtr(ids[i], ids[i]), score: float64(5 - i)}
	}
	out := q.applyRerankToRankedItems(context.Background(), "q", items)
	if len(out) != len(items) {
		t.Fatalf("length changed: %d vs %d", len(out), len(items))
	}
	got := make([]string, len(out))
	for i := range out {
		if out[i].fact == nil {
			t.Fatal("nil fact")
		}
		got[i] = out[i].fact.ID
	}
	want := append([]string(nil), got...)
	sort.Strings(want)
	base := append([]string(nil), ids...)
	sort.Strings(base)
	for i := range want {
		if want[i] != base[i] {
			t.Errorf("multiset mismatch: sorted got %v want %v", want, base)
			break
		}
	}
}

func TestRerank_SkipsWhenHeadHasHotMessage(t *testing.T) {
	called := false
	m := &mockReranker{fn: func(context.Context, string, []RankedItem, int) ([]RankedItem, error) {
		called = true
		return nil, nil
	}}
	q := New(nil, nil, nil, "", discardLogger(), WithReranker(m, 2))
	f := model.Fact{ID: "f1", Content: "x"}
	h := model.HotMessage{ID: "h1", Content: "hot"}
	in := []rankedItem{
		{hotMessage: &h, score: 1, isRawMessage: true, messageCitationPrefix: "hot:"},
		{fact: &f, score: 0.5},
	}
	out := q.applyRerankToRankedItems(context.Background(), "q", in)
	if called {
		t.Error("reranker should not run when prefix contains non-fact item")
	}
	if len(out) != 2 || out[0].hotMessage == nil {
		t.Fatalf("expected unchanged slice, got %#v", out)
	}
}

func TestCohereReranker_HTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/rerank" {
			t.Errorf("path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"index": 1, "relevance_score": 0.99},
				{"index": 0, "relevance_score": 0.5},
			},
		})
	}))
	defer ts.Close()

	_ = os.Setenv("COHERE_TEST_KEY", "k")
	defer os.Unsetenv("COHERE_TEST_KEY")

	cfg := model.ProviderConfig{
		Name:      "cohere",
		BaseURL:   ts.URL,
		Model:     "rerank-english-v3.0",
		APIKeyEnv: "COHERE_TEST_KEY",
	}
	c, err := NewCohereReranker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	items := []RankedItem{
		{Fact: model.Fact{ID: "x", Subject: "A", Content: "first"}, Score: 1},
		{Fact: model.Fact{ID: "y", Subject: "B", Content: "second"}, Score: 0.9},
	}
	out, err := c.Rerank(context.Background(), "q", items, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len %d", len(out))
	}
	if out[0].Fact.ID != "y" || out[1].Fact.ID != "x" {
		t.Errorf("order want y,x got %s,%s", out[0].Fact.ID, out[1].Fact.ID)
	}
}

package vecindex

import "testing"

func TestExactIndex_FirstAddAndSearch(t *testing.T) {
	idx := NewExactIndex(3)

	if err := idx.Add("fact:a", []float32{1, 0, 0}); err != nil {
		t.Fatalf("first Add(): %v", err)
	}
	hits, err := idx.Search([]float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search(): %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "fact:a" {
		t.Fatalf("expected exact hit for first add, got %+v", hits)
	}
}

func TestExactIndex_RepeatedAddAndPrefixFilter(t *testing.T) {
	idx := NewExactIndex(2)

	if err := idx.Add("fact:x", []float32{1, 0}); err != nil {
		t.Fatalf("Add fact:x: %v", err)
	}
	if err := idx.Add("chunk:y", []float32{0, 1}); err != nil {
		t.Fatalf("Add chunk:y: %v", err)
	}

	hits, err := idx.SearchWithPrefix([]float32{1, 0}, 5, "fact:")
	if err != nil {
		t.Fatalf("SearchWithPrefix(): %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "fact:x" {
		t.Fatalf("expected only fact:x, got %+v", hits)
	}
}

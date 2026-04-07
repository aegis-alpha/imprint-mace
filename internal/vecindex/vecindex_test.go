package vecindex

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	usearch "github.com/unum-cloud/usearch/golang"
)

func skipIfUSearchBroken(t *testing.T) {
	t.Helper()
	if os.Getenv("IMPRINT_SKIP_USEARCH") != "" {
		t.Skip("IMPRINT_SKIP_USEARCH set -- USearch C library crashes on this platform")
	}
}

func TestVectorIndex_Create(t *testing.T) {
	skipIfUSearchBroken(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.vecindex")
	u, err := OpenVectorIndex(path, 4, func() (map[string][]float32, error) {
		return map[string][]float32{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close() //nolint:errcheck
	if n := u.Len(); n != 0 {
		t.Fatalf("Len: want 0, got %d", n)
	}
}

func TestVectorIndex_AddAndSearch(t *testing.T) {
	skipIfUSearchBroken(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.vecindex")
	u, err := OpenVectorIndex(path, 3, func() (map[string][]float32, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close() //nolint:errcheck

	v := []float32{1, 0, 0}
	if err := u.Add("fact:a", v); err != nil {
		t.Fatal(err)
	}
	hits, err := u.Search(v, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "fact:a" {
		t.Fatalf("hits=%v", hits)
	}
}

func TestVectorIndex_PrefixFilter(t *testing.T) {
	skipIfUSearchBroken(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.vecindex")
	u, err := OpenVectorIndex(path, 2, func() (map[string][]float32, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close() //nolint:errcheck

	_ = u.Add("fact:x", []float32{1, 0})
	_ = u.Add("chunk:y", []float32{0, 1})

	q := []float32{1, 0}
	facts, err := u.SearchWithPrefix(q, 5, "fact:")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].ID != "fact:x" {
		t.Fatalf("fact prefix: %v", facts)
	}
	chunks, err := u.SearchWithPrefix(q, 5, "chunk:")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].ID != "chunk:y" {
		t.Fatalf("chunk prefix: %v", chunks)
	}
}

func TestVectorIndex_Remove(t *testing.T) {
	skipIfUSearchBroken(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.vecindex")
	u, err := OpenVectorIndex(path, 2, func() (map[string][]float32, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close() //nolint:errcheck

	_ = u.Add("fact:z", []float32{1, 0})
	if err := u.Remove("fact:z"); err != nil {
		t.Fatal(err)
	}
	hits, err := u.Search([]float32{1, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits after remove, got %v", hits)
	}
}

func TestVectorIndex_SaveAndReload(t *testing.T) {
	skipIfUSearchBroken(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dir := t.TempDir()
	path := filepath.Join(dir, "persist.vecindex")
	u, err := OpenVectorIndex(path, 2, func() (map[string][]float32, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	_ = u.Add("fact:p", []float32{0.3, 0.7})
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}

	u2, err := OpenVectorIndex(path, 2, func() (map[string][]float32, error) {
		return nil, fmt.Errorf("should not rebuild")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer u2.Close() //nolint:errcheck
	if !u2.Contains("fact:p") {
		t.Fatal("expected key after reload")
	}
}

func TestVectorIndex_RebuildFromSQLite(t *testing.T) {
	skipIfUSearchBroken(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dir := t.TempDir()
	path := filepath.Join(dir, "rebuild.vecindex")
	_ = os.Remove(path)
	data := map[string][]float32{
		"fact:1":  {1, 0},
		"chunk:2": {0, 1},
	}
	u, err := OpenVectorIndex(path, 2, func() (map[string][]float32, error) {
		return data, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close() //nolint:errcheck
	if u.Len() != 2 {
		t.Fatalf("Len want 2, got %d", u.Len())
	}
}

func TestVectorIndex_ConcurrentReadWrite(t *testing.T) {
	skipIfUSearchBroken(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dir := t.TempDir()
	path := filepath.Join(dir, "conc.vecindex")
	u, err := OpenVectorIndex(path, 4, func() (map[string][]float32, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close() //nolint:errcheck

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("fact:%d", i)
			_ = u.Add(id, []float32{float32(i), 0, 0, 0})
			_, _ = u.Search([]float32{1, 0, 0, 0}, 3)
		}(i)
	}
	wg.Wait()
}

type fakeNativeIndex struct {
	reserved uint
	added    map[usearch.Key][]float32

	reserveCalls int
	addCalls     int
	removeCalls  int
	clearCalls   int
}

func newFakeNativeIndex() *fakeNativeIndex {
	return &fakeNativeIndex{
		added: make(map[usearch.Key][]float32),
	}
}

func (f *fakeNativeIndex) Reserve(capacity uint) error {
	f.reserveCalls++
	if capacity > f.reserved {
		f.reserved = capacity
	}
	return nil
}

func (f *fakeNativeIndex) Add(key usearch.Key, vector []float32) error {
	f.addCalls++
	if uint(len(f.added)) >= f.reserved {
		return fmt.Errorf("fake native add without reserve: added=%d reserved=%d", len(f.added), f.reserved)
	}
	cp := append([]float32(nil), vector...)
	f.added[key] = cp
	return nil
}

func (f *fakeNativeIndex) Search(vector []float32, limit uint) ([]usearch.Key, []float32, error) {
	return nil, nil, nil
}

func (f *fakeNativeIndex) Remove(key usearch.Key) error {
	f.removeCalls++
	delete(f.added, key)
	return nil
}

func (f *fakeNativeIndex) Len() (uint, error) {
	return uint(len(f.added)), nil
}

func (f *fakeNativeIndex) SerializedLength() (uint, error) {
	return 0, nil
}

func (f *fakeNativeIndex) SaveBuffer([]byte, uint) error {
	return nil
}

func (f *fakeNativeIndex) LoadBuffer([]byte, uint) error {
	return nil
}

func (f *fakeNativeIndex) Destroy() error {
	return nil
}

func (f *fakeNativeIndex) ChangeThreadsSearch(uint) error {
	return nil
}

func (f *fakeNativeIndex) ChangeThreadsAdd(uint) error {
	return nil
}

func (f *fakeNativeIndex) Clear() error {
	f.clearCalls++
	f.added = make(map[usearch.Key][]float32)
	return nil
}

func TestUSearchIndex_AddReservesFreshIndexBeforeFirstWrite(t *testing.T) {
	native := newFakeNativeIndex()
	idx := newUSearchIndexWithNative(native, 3, filepath.Join(t.TempDir(), "fresh.vecindex"))

	if err := idx.Add("fact:fresh", []float32{1, 0, 0}); err != nil {
		t.Fatalf("first Add() should reserve before writing: %v", err)
	}
	if native.reserveCalls == 0 {
		t.Fatal("expected first Add() to reserve native capacity")
	}
	if native.addCalls != 1 {
		t.Fatalf("expected one native add, got %d", native.addCalls)
	}
}

func TestUSearchIndex_AddGrowsCapacityAcrossRepeatedWrites(t *testing.T) {
	native := newFakeNativeIndex()
	idx := newUSearchIndexWithNative(native, 3, filepath.Join(t.TempDir(), "grow.vecindex"))

	if err := idx.Add("fact:1", []float32{1, 0, 0}); err != nil {
		t.Fatalf("first Add(): %v", err)
	}
	if err := idx.Add("fact:2", []float32{0, 1, 0}); err != nil {
		t.Fatalf("second Add() should grow capacity before writing: %v", err)
	}
	if native.reserved < 2 {
		t.Fatalf("expected reserved capacity >= 2 after repeated adds, got %d", native.reserved)
	}
}

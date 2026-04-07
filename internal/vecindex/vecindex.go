// Package vecindex wraps USearch for approximate vector search with string IDs.
package vecindex

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"strings"
	"sync"

	usearch "github.com/unum-cloud/usearch/golang"
)

// ScoredID is one search hit: logical prefixed id ("fact:…", "chunk:…") and distance.
type ScoredID struct {
	ID       string
	Distance float64
}

type nativeIndex interface {
	Reserve(capacity uint) error
	Add(key usearch.Key, vector []float32) error
	Search(vector []float32, limit uint) ([]usearch.Key, []float32, error)
	Remove(key usearch.Key) error
	Len() (uint, error)
	SerializedLength() (uint, error)
	SaveBuffer(buf []byte, size uint) error
	LoadBuffer(buf []byte, size uint) error
	Destroy() error
	ChangeThreadsSearch(threads uint) error
	ChangeThreadsAdd(threads uint) error
	Clear() error
}

// VectorIndex is the persistence-agnostic vector ANN API used by the store.
type VectorIndex interface {
	Add(id string, vector []float32) error
	Search(vector []float32, k int) ([]ScoredID, error)
	SearchWithPrefix(vector []float32, k int, prefix string) ([]ScoredID, error)
	Remove(id string) error
	Contains(id string) bool
	Len() int
	Save() error
	Close() error
	// ResetFromEmbeddings replaces all indexed vectors (e.g. after admin reset).
	ResetFromEmbeddings(m map[string][]float32) error
}

const fileMagic = "IMV1"

// USearchIndex implements VectorIndex using USearch HNSW with F16 storage and cosine metric.
type USearchIndex struct {
	mu        sync.RWMutex
	idx       nativeIndex
	keyToID   map[usearch.Key]string
	idToKey   map[string]usearch.Key
	dims      int
	cachePath string
	reserved  uint
}

func newUSearchIndexWithNative(idx nativeIndex, dims int, cachePath string) *USearchIndex {
	return &USearchIndex{
		idx:       idx,
		keyToID:   make(map[usearch.Key]string),
		idToKey:   make(map[string]usearch.Key),
		dims:      dims,
		cachePath: cachePath,
	}
}

// OpenVectorIndex loads a composite cache file or rebuilds from rebuildFunc.
func OpenVectorIndex(cachePath string, dims int, rebuildFunc func() (map[string][]float32, error)) (*USearchIndex, error) {
	if dims <= 0 {
		return nil, errors.New("vecindex: dimensions must be positive")
	}
	if rebuildFunc == nil {
		return nil, errors.New("vecindex: rebuildFunc is required")
	}

	conf := usearch.DefaultConfig(uint(dims))
	conf.Metric = usearch.Cosine
	conf.Quantization = usearch.F16

	u := newUSearchIndexWithNative(nil, dims, cachePath)

	data, err := os.ReadFile(cachePath)
	if err == nil && len(data) > 4 {
		if string(data[0:4]) == fileMagic {
			if err := u.loadFromCompound(data, conf); err == nil {
				_ = u.idx.ChangeThreadsSearch(16) //nolint:errcheck // best-effort tuning
				_ = u.idx.ChangeThreadsAdd(4)     //nolint:errcheck
				return u, nil
			}
		}
	}

	idx, err := usearch.NewIndex(conf)
	if err != nil {
		return nil, fmt.Errorf("vecindex: new index: %w", err)
	}
	u.idx = idx
	_ = u.idx.ChangeThreadsSearch(16) //nolint:errcheck
	_ = u.idx.ChangeThreadsAdd(4)     //nolint:errcheck

	m, err := rebuildFunc()
	if err != nil {
		_ = u.idx.Destroy() //nolint:errcheck
		return nil, fmt.Errorf("vecindex: rebuild: %w", err)
	}
	if err := u.importEmbeddingsLocked(m); err != nil {
		_ = u.idx.Destroy() //nolint:errcheck
		return nil, err
	}
	if err := u.saveCompoundUnlocked(); err != nil {
		_ = u.idx.Destroy() //nolint:errcheck
		return nil, err
	}
	return u, nil
}

func (u *USearchIndex) loadFromCompound(data []byte, conf usearch.IndexConfig) error {
	if len(data) < 4+8 {
		return errors.New("vecindex: corrupt cache (short header)")
	}
	off := 4
	ulen := int(binary.LittleEndian.Uint64(data[off:]))
	off += 8
	if off+ulen+8 > len(data) {
		return errors.New("vecindex: corrupt cache (usearch slice)")
	}
	ublob := data[off : off+ulen]
	off += ulen
	jlen := int(binary.LittleEndian.Uint64(data[off:]))
	off += 8
	if off+jlen != len(data) {
		return errors.New("vecindex: corrupt cache (json slice)")
	}
	jsonPart := data[off : off+jlen]

	idx, err := usearch.NewIndex(conf)
	if err != nil {
		return err
	}
	if err := idx.LoadBuffer(ublob, uint(len(ublob))); err != nil {
		_ = idx.Destroy() //nolint:errcheck
		return err
	}
	var keyMap map[string]string
	if err := json.Unmarshal(jsonPart, &keyMap); err != nil {
		_ = idx.Destroy() //nolint:errcheck
		return err
	}
	u.idx = idx
	u.keyToID = make(map[usearch.Key]string, len(keyMap))
	u.idToKey = make(map[string]usearch.Key, len(keyMap))
	for ks, logical := range keyMap {
		kv, err := strconv.ParseUint(ks, 10, 64)
		if err != nil {
			_ = u.idx.Destroy() //nolint:errcheck
			return fmt.Errorf("vecindex: bad key in cache: %w", err)
		}
		k := usearch.Key(kv)
		u.keyToID[k] = logical
		u.idToKey[logical] = k
	}
	u.reserved = uint(len(keyMap))
	return nil
}

func (u *USearchIndex) hashID(id string) usearch.Key {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	for {
		sum := h.Sum64()
		k := usearch.Key(sum)
		if exist, ok := u.keyToID[k]; !ok || exist == id {
			return k
		}
		_, _ = h.Write([]byte{0})
	}
}

func (u *USearchIndex) importEmbeddingsLocked(m map[string][]float32) error {
	if len(m) == 0 {
		return nil
	}
	for id, vec := range m {
		if len(vec) != u.dims {
			return fmt.Errorf("vecindex: dimension mismatch for %q: got %d want %d", id, len(vec), u.dims)
		}
		if err := u.ensureWriteCapacityLocked(len(u.idToKey) + 1); err != nil {
			return err
		}
		k := u.hashID(id)
		u.keyToID[k] = id
		u.idToKey[id] = k
		if err := u.idx.Add(k, vec); err != nil {
			return fmt.Errorf("vecindex: add %q: %w", id, err)
		}
	}
	return nil
}

func (u *USearchIndex) ensureWriteCapacityLocked(nextCount int) error {
	if nextCount <= 0 {
		return nil
	}
	if uint(nextCount) <= u.reserved {
		return nil
	}
	target := u.reserved
	if target == 0 {
		target = 1
	}
	for target < uint(nextCount) {
		target *= 2
	}
	if err := u.idx.Reserve(target); err != nil {
		return fmt.Errorf("vecindex: reserve: %w", err)
	}
	u.reserved = target
	return nil
}

// Add implements VectorIndex.
func (u *USearchIndex) Add(id string, vector []float32) error {
	if len(vector) != u.dims {
		return fmt.Errorf("vecindex: add %q: dimension mismatch", id)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if old, had := u.idToKey[id]; had {
		delete(u.keyToID, old)
		_ = u.idx.Remove(old) //nolint:errcheck // replace path
	}
	if err := u.ensureWriteCapacityLocked(len(u.idToKey) + 1); err != nil {
		return err
	}
	k := u.hashID(id)
	u.keyToID[k] = id
	u.idToKey[id] = k
	return u.idx.Add(k, vector)
}

// Search implements VectorIndex.
func (u *USearchIndex) Search(vector []float32, k int) ([]ScoredID, error) {
	return u.SearchWithPrefix(vector, k, "")
}

// SearchWithPrefix implements VectorIndex. Empty prefix means no filter.
func (u *USearchIndex) SearchWithPrefix(vector []float32, k int, prefix string) ([]ScoredID, error) {
	if k <= 0 {
		return nil, nil
	}
	u.mu.RLock()
	defer u.mu.RUnlock()

	var keys []usearch.Key
	var dists []float32
	var err error
	if prefix == "" {
		keys, dists, err = u.idx.Search(vector, uint(k))
	} else {
		// FilteredSearch with closure causes CGO pointer panics in Go 1.21+
		// Instead: search all, then filter results
		allK := k * 3 // oversample to get enough prefix matches
		if allK > 100 {
			allK = 100
		}
		keys, dists, err = u.idx.Search(vector, uint(allK))
		if err != nil {
			return nil, err
		}
		// Filter to prefix matches
		filtered := make([]usearch.Key, 0, k)
		filteredDists := make([]float32, 0, k)
		for i := range keys {
			logical, ok := u.keyToID[keys[i]]
			if ok && strings.HasPrefix(logical, prefix) {
				filtered = append(filtered, keys[i])
				filteredDists = append(filteredDists, dists[i])
				if len(filtered) >= k {
					break
				}
			}
		}
		keys = filtered
		dists = filteredDists
	}
	if err != nil {
		return nil, err
	}
	out := make([]ScoredID, 0, len(keys))
	for i := range keys {
		logical, ok := u.keyToID[keys[i]]
		if !ok {
			continue
		}
		// prefix filtering already done in else branch above
		out = append(out, ScoredID{ID: logical, Distance: float64(dists[i])})
	}
	return out, nil
}

// Remove implements VectorIndex.
func (u *USearchIndex) Remove(id string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	k, ok := u.idToKey[id]
	if !ok {
		return nil
	}
	delete(u.idToKey, id)
	delete(u.keyToID, k)
	return u.idx.Remove(k)
}

// Contains implements VectorIndex.
func (u *USearchIndex) Contains(id string) bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	_, ok := u.idToKey[id]
	return ok
}

// Len implements VectorIndex.
func (u *USearchIndex) Len() int {
	u.mu.RLock()
	defer u.mu.RUnlock()
	n, err := u.idx.Len()
	if err != nil {
		return len(u.idToKey)
	}
	return int(n)
}

// Save implements VectorIndex.
func (u *USearchIndex) Save() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.saveCompoundUnlocked()
}

func (u *USearchIndex) saveCompoundUnlocked() error {
	if u.idx == nil {
		return errors.New("vecindex: save: nil index")
	}
	slen, err := u.idx.SerializedLength()
	if err != nil {
		return err
	}
	ublob := make([]byte, slen)
	if err := u.idx.SaveBuffer(ublob, slen); err != nil {
		return err
	}
	keyMap := make(map[string]string, len(u.keyToID))
	for k, logical := range u.keyToID {
		keyMap[strconv.FormatUint(uint64(k), 10)] = logical
	}
	jsonPart, err := json.Marshal(keyMap)
	if err != nil {
		return err
	}
	var buf []byte
	buf = append(buf, []byte(fileMagic)...)
	var ulen [8]byte
	binary.LittleEndian.PutUint64(ulen[:], uint64(len(ublob)))
	buf = append(buf, ulen[:]...)
	buf = append(buf, ublob...)
	var jlen [8]byte
	binary.LittleEndian.PutUint64(jlen[:], uint64(len(jsonPart)))
	buf = append(buf, jlen[:]...)
	buf = append(buf, jsonPart...)
	tmp := u.cachePath + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, u.cachePath)
}

// Close implements VectorIndex: Save then destroy native index.
func (u *USearchIndex) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.idx == nil {
		return nil
	}
	var saveErr error
	if u.cachePath != "" {
		saveErr = u.saveCompoundUnlocked()
	}
	destErr := u.idx.Destroy()
	u.idx = nil
	u.keyToID = nil
	u.idToKey = nil
	return errors.Join(saveErr, destErr)
}

// ResetFromEmbeddings implements VectorIndex.
func (u *USearchIndex) ResetFromEmbeddings(m map[string][]float32) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.idx == nil {
		return errors.New("vecindex: reset: nil index")
	}
	if err := u.idx.Clear(); err != nil {
		return err
	}
	u.keyToID = make(map[usearch.Key]string)
	u.idToKey = make(map[string]usearch.Key)
	u.reserved = 0
	return u.importEmbeddingsLocked(m)
}

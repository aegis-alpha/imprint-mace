// Package segment implements topic segmentation for cooldown messages.
//
// The Hybrid Union algorithm combines two complementary approaches:
//   - TreeSeg: hierarchical divisive clustering (top-down, finds global structure)
//   - TT+Merge: TextTiling with small-segment merging (bottom-up, finds local shifts)
//
// Boundary union: if either algorithm places a boundary, it is kept.
// This prefers over-segmentation per D32/R4 research findings.
//
// Research basis: S060 simulation showed Hybrid Union ARI=0.7494 vs
// TreeSeg-alone ARI=0.7307 on a 251-message transcript.
package segment

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// Params holds tunable parameters for the Hybrid Union algorithm.
type Params struct {
	// TreeSeg parameters
	TreeLambda float64 // balance penalty for splits (default 0.001)
	TreeK      int     // target number of segments (default 7)

	// TT+Merge parameters
	TTWindow    int     // sliding window size for block similarity (default 3)
	TTThreshold float64 // similarity drop threshold for boundary detection (default 0.5)
	TTMinSize   int     // minimum segment size before merging (default 5)
}

// DefaultParams returns the parameters validated by S060/S061 simulation.
func DefaultParams() Params {
	return Params{
		TreeLambda:  0.001,
		TreeK:       7,
		TTWindow:    3,
		TTThreshold: 0.5,
		TTMinSize:   5,
	}
}

// SegmentResult maps cluster IDs to the message IDs they contain.
// Each cluster represents a contiguous topic segment.
type SegmentResult struct {
	Clusters map[string][]string // clusterID -> []messageID
	Order    []string            // cluster IDs in chronological order
}

// HybridUnion runs the combined TreeSeg + TT+Merge algorithm on messages.
// Messages must be sorted chronologically (by Timestamp).
// The algorithm is pure -- no DB dependencies.
//
// If messages have no embeddings (all nil/empty in the sims slice),
// similarities default to 0.5 (neutral) and segmentation relies on
// TT+Merge's threshold-based approach. This is a known v0.6.0 limitation.
func HybridUnion(messages []model.CooldownMessage, embeddings [][]float32, params Params) SegmentResult {
	n := len(messages)
	if n == 0 {
		return SegmentResult{Clusters: map[string][]string{}, Order: nil}
	}

	sims := adjacentSimilarities(embeddings, n)

	treeBoundaries := treeSeg(sims, n, params.TreeLambda, params.TreeK)
	ttBoundaries := ttMerge(sims, n, params.TTWindow, params.TTMinSize, params.TTThreshold)

	boundaries := unionBoundaries(treeBoundaries, ttBoundaries)

	return buildClusters(messages, boundaries)
}

// adjacentSimilarities computes cosine similarity between consecutive messages.
// Returns a slice of length n-1 where sims[i] = similarity(msg[i], msg[i+1]).
// Messages without embeddings get similarity 0.5 (neutral fallback).
func adjacentSimilarities(embeddings [][]float32, n int) []float64 {
	if n <= 1 {
		return nil
	}
	sims := make([]float64, n-1)
	for i := 0; i < n-1; i++ {
		if i < len(embeddings) && i+1 < len(embeddings) &&
			len(embeddings[i]) > 0 && len(embeddings[i+1]) > 0 {
			sims[i] = cosineSimilarity(embeddings[i], embeddings[i+1])
		} else {
			sims[i] = 0.5
		}
	}
	return sims
}

// cosineSimilarity computes cosine similarity between two float32 vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.5
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0.5
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// treeSeg implements hierarchical divisive segmentation.
//
// Algorithm: recursively split the segment with the highest variance reduction,
// penalized by a balance factor (lambda). Stops when K segments are reached.
//
// At each step, for each existing segment, find the split point that maximizes:
//   gain = variance_reduction - lambda * balance_penalty
// where balance_penalty = |left_size - right_size| / segment_size
//
// The segment with the best gain is split. Repeat until K segments exist.
func treeSeg(sims []float64, n int, lambda float64, k int) []int {
	if n <= 1 || k <= 1 {
		return nil
	}
	if k >= n {
		boundaries := make([]int, n-1)
		for i := range boundaries {
			boundaries[i] = i + 1
		}
		return boundaries
	}

	type segment struct {
		start, end int // [start, end) indices into messages
	}

	segments := []segment{{0, n}}
	var boundaries []int

	for len(segments) < k {
		bestGain := math.Inf(-1)
		bestSeg := -1
		bestSplit := -1

		for si, seg := range segments {
			segLen := seg.end - seg.start
			if segLen < 2 {
				continue
			}

			segSims := sims[seg.start : seg.end-1]
			segVar := variance(segSims)
			if segVar == 0 {
				continue
			}

			for sp := 1; sp < segLen; sp++ {
				leftSims := segSims[:sp]
				rightSims := segSims[sp:]

				leftVar := variance(leftSims)
				rightVar := variance(rightSims)

				leftWeight := float64(len(leftSims)) / float64(len(segSims))
				rightWeight := float64(len(rightSims)) / float64(len(segSims))
				weightedVar := leftWeight*leftVar + rightWeight*rightVar

				varReduction := segVar - weightedVar

				balancePenalty := math.Abs(float64(sp)-float64(segLen-sp)) / float64(segLen)
				gain := varReduction - lambda*balancePenalty

				if gain > bestGain {
					bestGain = gain
					bestSeg = si
					bestSplit = seg.start + sp
				}
			}
		}

		if bestSeg < 0 {
			break
		}

		old := segments[bestSeg]
		left := segment{old.start, bestSplit}
		right := segment{bestSplit, old.end}

		newSegs := make([]segment, 0, len(segments)+1)
		newSegs = append(newSegs, segments[:bestSeg]...)
		newSegs = append(newSegs, left, right)
		newSegs = append(newSegs, segments[bestSeg+1:]...)
		segments = newSegs

		boundaries = append(boundaries, bestSplit)
	}

	sort.Ints(boundaries)
	return boundaries
}

// ttMerge implements TextTiling with a sliding window + small-segment merge.
//
// Step 1 (TextTiling): compute block similarity using a sliding window.
// A boundary is placed where similarity drops below the threshold.
//
// Step 2 (Merge): segments smaller than minSize are merged into their
// most similar neighbor (by average similarity at the boundary).
func ttMerge(sims []float64, n int, window, minSize int, threshold float64) []int {
	if n <= 1 || window < 1 {
		return nil
	}

	blockSims := blockSimilarities(sims, window)

	var rawBoundaries []int
	for i, bs := range blockSims {
		if bs < threshold {
			rawBoundaries = append(rawBoundaries, i+1)
		}
	}

	return mergeSmallSegments(rawBoundaries, sims, n, minSize)
}

// blockSimilarities computes TextTiling block similarity scores.
// For position i, compares the window of similarities before i with the window after i.
// Returns a slice of length len(sims)-1 (or 0 if sims is too short).
func blockSimilarities(sims []float64, window int) []float64 {
	if len(sims) < 2 {
		return nil
	}

	result := make([]float64, 0, len(sims)-1)
	for i := 1; i < len(sims); i++ {
		leftStart := i - window
		if leftStart < 0 {
			leftStart = 0
		}
		rightEnd := i + window
		if rightEnd > len(sims) {
			rightEnd = len(sims)
		}

		leftBlock := sims[leftStart:i]
		rightBlock := sims[i:rightEnd]

		leftMean := mean(leftBlock)
		rightMean := mean(rightBlock)

		result = append(result, (leftMean+rightMean)/2)
	}
	return result
}

// mergeSmallSegments consolidates segments smaller than minSize into neighbors.
// Merging is done by removing the boundary that produces the smallest segment,
// repeated until all segments meet the minimum size.
func mergeSmallSegments(boundaries []int, sims []float64, n, minSize int) []int {
	if minSize <= 1 || len(boundaries) == 0 {
		return boundaries
	}

	result := make([]int, len(boundaries))
	copy(result, boundaries)

	for {
		segSizes := segmentSizes(result, n)
		smallestIdx := -1
		smallestSize := math.MaxInt64

		for i, sz := range segSizes {
			if sz < minSize && sz < smallestSize {
				smallestSize = sz
				smallestIdx = i
			}
		}

		if smallestIdx < 0 {
			break
		}

		// Decide which boundary to remove: the one before or after the small segment.
		// Remove the boundary with higher similarity (less likely a real topic break).
		var removeIdx int
		switch {
		case smallestIdx == 0:
			removeIdx = 0
		case smallestIdx == len(result):
			removeIdx = len(result) - 1
		default:
			leftBound := result[smallestIdx-1]
			rightBound := result[smallestIdx]
			leftSim := 0.0
			rightSim := 0.0
			if leftBound > 0 && leftBound-1 < len(sims) {
				leftSim = sims[leftBound-1]
			}
			if rightBound > 0 && rightBound-1 < len(sims) {
				rightSim = sims[rightBound-1]
			}
			if leftSim >= rightSim {
				removeIdx = smallestIdx - 1
			} else {
				removeIdx = smallestIdx
			}
		}

		result = append(result[:removeIdx], result[removeIdx+1:]...)
	}

	return result
}

// segmentSizes returns the size of each segment defined by the boundaries.
// boundaries are indices into the message array where segments start.
// Returns len(boundaries)+1 sizes.
func segmentSizes(boundaries []int, n int) []int {
	sizes := make([]int, len(boundaries)+1)
	prev := 0
	for i, b := range boundaries {
		sizes[i] = b - prev
		prev = b
	}
	sizes[len(boundaries)] = n - prev
	return sizes
}

// unionBoundaries merges two sorted boundary lists, removing duplicates.
func unionBoundaries(a, b []int) []int {
	set := make(map[int]struct{}, len(a)+len(b))
	for _, v := range a {
		set[v] = struct{}{}
	}
	for _, v := range b {
		set[v] = struct{}{}
	}
	result := make([]int, 0, len(set))
	for v := range set {
		result = append(result, v)
	}
	sort.Ints(result)
	return result
}

// buildClusters assigns messages to clusters based on boundaries.
// Each contiguous segment between boundaries becomes a cluster with a new ULID.
func buildClusters(messages []model.CooldownMessage, boundaries []int) SegmentResult {
	n := len(messages)
	if n == 0 {
		return SegmentResult{Clusters: map[string][]string{}, Order: nil}
	}

	starts := make([]int, 0, len(boundaries)+1)
	starts = append(starts, 0)
	starts = append(starts, boundaries...)

	clusters := make(map[string][]string, len(starts))
	order := make([]string, 0, len(starts))

	for i, start := range starts {
		end := n
		if i+1 < len(starts) {
			end = starts[i+1]
		}

		clusterID := db.NewID()
		ids := make([]string, 0, end-start)
		for j := start; j < end; j++ {
			ids = append(ids, messages[j].ID)
		}
		clusters[clusterID] = ids
		order = append(order, clusterID)
	}

	return SegmentResult{Clusters: clusters, Order: order}
}

// --- Integration adapter ---

// ClusterUnclustered loads unclustered cooldown messages for a single session,
// runs Hybrid Union segmentation, and writes cluster assignments back to the store.
//
// Clustering is per-session (platform_session_id). Cross-session clustering
// is a correctness bug -- this function enforces the invariant by only loading
// messages for the given session.
//
// The embeddings parameter is optional. If nil, the algorithm degrades gracefully
// (all similarities = 0.5). Callers that have embeddings available should pass them
// aligned 1:1 with the messages returned by ListCooldownUnclustered.
func ClusterUnclustered(
	ctx context.Context,
	store db.Store,
	platformSessionID string,
	limit int,
	embeddings [][]float32,
	params Params,
) (*SegmentResult, error) {
	messages, err := store.ListCooldownUnclustered(ctx, platformSessionID, limit)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}

	result := HybridUnion(messages, embeddings, params)

	for clusterID, msgIDs := range result.Clusters {
		if err := store.AssignCooldownCluster(ctx, clusterID, msgIDs); err != nil {
			return nil, err
		}
	}

	return &result, nil
}

// --- Utility functions ---

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func variance(vals []float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	m := mean(vals)
	sum := 0.0
	for _, v := range vals {
		d := v - m
		sum += d * d
	}
	return sum / float64(len(vals))
}

// jaccardSimilarity computes word-level Jaccard similarity between two texts.
// Reserved for future use when text-only degradation is needed beyond the
// neutral-0.5 fallback.
func jaccardSimilarity(a, b string) float64 {
	wordsA := wordSet(a)
	wordsB := wordSet(b)
	if len(wordsA) == 0 && len(wordsB) == 0 {
		return 1.0
	}
	intersection := 0
	for w := range wordsA {
		if _, ok := wordsB[w]; ok {
			intersection++
		}
	}
	union := len(wordsA) + len(wordsB) - intersection
	if union == 0 {
		return 1.0
	}
	return float64(intersection) / float64(union)
}

func wordSet(s string) map[string]struct{} {
	words := strings.Fields(strings.ToLower(s))
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}

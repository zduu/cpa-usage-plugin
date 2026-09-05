package main

import (
	"math/rand"
	"testing"
	"time"
)

type matchingObjective struct {
	count    int
	strength int64
	distance int64
}

func objectiveForMatching(matched map[int]int, candidates map[int]map[int]protocolCorrelationEdgeResult) matchingObjective {
	objective := matchingObjective{}
	for fallback, native := range matched {
		edge, ok := candidates[fallback][native]
		if !ok {
			continue
		}
		objective.count++
		objective.strength += edge.Strength
		objective.distance += edge.Distance.Nanoseconds()
	}
	return objective
}

func betterMatchingObjective(left, right matchingObjective) bool {
	if left.count != right.count {
		return left.count > right.count
	}
	if left.strength != right.strength {
		return left.strength > right.strength
	}
	return left.distance < right.distance
}

func bruteForceMatching(fallbacks, natives []int, candidates map[int]map[int]protocolCorrelationEdgeResult) matchingObjective {
	best := matchingObjective{count: -1}
	var visit func(int, map[int]struct{}, matchingObjective)
	visit = func(position int, used map[int]struct{}, current matchingObjective) {
		if position == len(fallbacks) {
			if betterMatchingObjective(current, best) {
				best = current
			}
			return
		}
		// Leaving a fallback unmatched is necessary when the graph is not
		// complete; cardinality is still optimized before strength and distance.
		visit(position+1, used, current)
		for native := range candidates[fallbacks[position]] {
			if _, exists := used[native]; exists {
				continue
			}
			edge := candidates[fallbacks[position]][native]
			used[native] = struct{}{}
			visit(position+1, used, matchingObjective{
				count:    current.count + 1,
				strength: current.strength + edge.Strength,
				distance: current.distance + edge.Distance.Nanoseconds(),
			})
			delete(used, native)
		}
	}
	visit(0, make(map[int]struct{}), matchingObjective{})
	return best
}

func TestProtocolMinimumCostMatchingPrioritizesCardinalityStrengthAndDistance(t *testing.T) {
	fallbacks := []int{0, 1}
	natives := []int{10, 11}
	observations := make(map[int]protocolCorrelationObservation)
	for i, id := range append(append([]int(nil), fallbacks...), natives...) {
		observations[id] = protocolCorrelationObservation{StableOrder: protocolStableOrder{Index: i}}
	}
	candidates := map[int]map[int]protocolCorrelationEdgeResult{
		0: {
			10: {Strength: 100, Distance: 4},
			11: {Strength: 1, Distance: 100},
		},
		1: {
			10: {Strength: 1, Distance: 100},
			11: {Strength: 100, Distance: 4},
		},
	}
	matched := protocolMinimumCostMatching(fallbacks, natives, candidates, observations, nil)
	if got := objectiveForMatching(matched, candidates); got != (matchingObjective{count: 2, strength: 200, distance: 8}) {
		t.Fatalf("matching objective = %#v, want cardinality/strength/distance 2/200/8", got)
	}

	// The maximum-cardinality solution must win even if the one-edge solution
	// has a much stronger and closer individual edge.
	candidates = map[int]map[int]protocolCorrelationEdgeResult{
		0: {10: {Strength: 1000, Distance: 1}, 11: {Strength: 1, Distance: 10}},
		1: {10: {Strength: 1, Distance: 10}},
	}
	matched = protocolMinimumCostMatching(fallbacks, natives, candidates, observations, nil)
	if got := objectiveForMatching(matched, candidates); got.count != 2 {
		t.Fatalf("matching cardinality = %d, want 2 (matched=%v)", got.count, matched)
	}
}

func TestProtocolMinimumCostMatchingIsStableAcrossInputOrder(t *testing.T) {
	const size = 4
	fallbacks := []int{0, 1, 2, 3}
	natives := []int{10, 11, 12, 13}
	observations := make(map[int]protocolCorrelationObservation)
	for i, id := range natives {
		observations[id] = protocolCorrelationObservation{StableOrder: protocolStableOrder{Index: i}}
	}
	candidates := make(map[int]map[int]protocolCorrelationEdgeResult, size)
	for _, fallback := range fallbacks {
		candidates[fallback] = make(map[int]protocolCorrelationEdgeResult, size)
		for _, native := range natives {
			// Every edge is unique, so the objective has no tie that could
			// legitimately depend on input ordering.
			strength := int64(10 + ((fallback+1)*(native-9)*7)%37)
			distance := int64(1 + ((fallback*11 + (native-10)*5) % 29))
			candidates[fallback][native] = protocolCorrelationEdgeResult{
				Strength: strength,
				Distance: timeDurationNanoseconds(distance),
			}
		}
	}
	// Compare the flow result with exhaustive search first, then repeat with
	// shuffled fallback/native slices. This exercises both maximum-cardinality
	// augmenting paths and the deterministic native ordering used for ties.
	want := bruteForceMatching(fallbacks, natives, candidates)
	for iteration := 0; iteration < 100; iteration++ {
		fallbackOrder := append([]int(nil), fallbacks...)
		nativeOrder := append([]int(nil), natives...)
		random := rand.New(rand.NewSource(int64(iteration + 1)))
		random.Shuffle(len(fallbackOrder), func(i, j int) { fallbackOrder[i], fallbackOrder[j] = fallbackOrder[j], fallbackOrder[i] })
		random.Shuffle(len(nativeOrder), func(i, j int) { nativeOrder[i], nativeOrder[j] = nativeOrder[j], nativeOrder[i] })
		got := objectiveForMatching(protocolMinimumCostMatching(fallbackOrder, nativeOrder, candidates, observations, nil), candidates)
		if got != want {
			t.Fatalf("iteration %d objective = %#v, want %#v", iteration, got, want)
		}
	}
}

// protocolMatching tests use small integral distances without coupling their
// meaning to a wall-clock timestamp. Keep the conversion explicit at the call
// site so the production matcher still receives a time.Duration.
func timeDurationNanoseconds(value int64) time.Duration {
	return time.Duration(value)
}

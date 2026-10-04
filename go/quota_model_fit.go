package main

import (
	"math"
	"sort"
)

type quotaFittedCapacity struct {
	tokens    float64
	low, high float64
	cost      *float64
}

// Fit quota fraction = sum(model tokens in millions * model quota coefficient).
// Prices never enter the fit: changing prices only changes the dollar conversion.
// Column scaling allows small-volume models to participate without being lost
// merely because another model has more recorded tokens.
func fitQuotaMixedModels(intervals []quotaModelInterval) map[string]quotaFittedCapacity {
	totals := make(map[string]quotaIntervalModel)
	hasMixed := false
	for _, interval := range intervals {
		contributors := 0
		for name, model := range interval.models {
			if model.tokens <= 0 {
				continue
			}
			contributors++
			total := totals[name]
			total.tokens += model.tokens
			total.cost = addNonNegativeCost(total.cost, model.cost)
			total.unpriced = total.unpriced || model.unpriced
			totals[name] = total
		}
		hasMixed = hasMixed || contributors > 1
	}
	n := len(totals)
	// One-model periods already have a direct ratio estimate. A joint fit
	// needs at least one extra observation to assess residual uncertainty.
	if !hasMixed || n < 2 || len(intervals) <= n {
		return nil
	}
	names := make([]string, 0, n)
	for name := range totals {
		names = append(names, name)
	}
	sort.Strings(names)
	x := make([][]float64, len(intervals))
	y := make([]float64, len(intervals))
	scale := make([]float64, n)
	for i, interval := range intervals {
		x[i] = make([]float64, n)
		y[i] = interval.delta
		for j, name := range names {
			x[i][j] = interval.models[name].tokens / 1e6
			scale[j] += x[i][j] * x[i][j]
		}
	}
	for j := range scale {
		scale[j] = math.Sqrt(scale[j])
		if scale[j] <= 0 || math.IsInf(scale[j], 0) || math.IsNaN(scale[j]) {
			return nil
		}
		for i := range x {
			x[i][j] /= scale[j]
		}
	}
	g := make([][]float64, n)
	h := make([]float64, n)
	for j := range g {
		g[j] = make([]float64, n)
		for i := range x {
			h[j] += x[i][j] * y[i]
			for k := range g[j] {
				g[j][k] += x[i][j] * x[i][k]
			}
		}
	}
	l := quotaFitCholesky(g)
	if l == nil {
		// Equal or nearly equal model proportions cannot identify the
		// individual coefficients; do not invent differences using prices.
		return nil
	}
	beta := quotaFitSolve(l, h)
	negative := false
	for j := range beta {
		if beta[j] < 0 {
			negative = true
			beta[j] = 0
		}
	}
	if negative {
		// Nonnegative least squares by cyclic coordinate descent. Start
		// from the unconstrained solution and require convergence.
		converged := false
		for iteration := 0; iteration < 2000; iteration++ {
			change, largest := 0.0, 0.0
			for j := range beta {
				value := h[j]
				for k, b := range beta {
					if k != j {
						value -= g[j][k] * b
					}
				}
				value = math.Max(0, value/g[j][j])
				change = math.Max(change, math.Abs(value-beta[j]))
				largest = math.Max(largest, value)
				beta[j] = value
			}
			if change <= 1e-10*math.Max(largest, 1e-12) {
				converged = true
				break
			}
		}
		if !converged {
			return nil
		}
	}
	sse := 0.0
	for i, row := range x {
		predicted := 0.0
		for j, value := range row {
			predicted += value * beta[j]
		}
		sse += (predicted - y[i]) * (predicted - y[i])
	}
	sigma := math.Sqrt(sse / float64(len(x)-n))
	out := make(map[string]quotaFittedCapacity)
	for j, name := range names {
		// Approximate residual-based uncertainty is a rejection check,
		// not a reported confidence guarantee for delayed quota signals.
		unit := make([]float64, n)
		unit[j] = 1
		inverse := quotaFitSolve(l, unit)
		errorMargin := 2 * sigma * math.Sqrt(math.Max(0, inverse[j]))
		// Bound the effect of rounding errors through the least-squares
		// influence matrix. Shared endpoints cancel only within a contiguous run.
		roundingMargin, previousWeight := 0.0, 0.0
		for i, row := range x {
			weight := 0.0
			for k, value := range row {
				weight += inverse[k] * value
			}
			if i > 0 && !intervals[i].start.IsZero() && intervals[i-1].end.Equal(intervals[i].start) {
				roundingMargin += math.Abs(previousWeight-weight) * quotaModelQuantum / 2
			} else {
				roundingMargin += (math.Abs(previousWeight) + math.Abs(weight)) * quotaModelQuantum / 2
			}
			previousWeight = weight
		}
		roundingMargin += math.Abs(previousWeight) * quotaModelQuantum / 2
		errorMargin = math.Max(errorMargin, roundingMargin)
		if beta[j]/scale[j]*totals[name].tokens/1e6+1e-12 < quotaModelMinDelta {
			continue
		}
		if beta[j] <= errorMargin || beta[j] <= 0 || math.IsNaN(beta[j]) || math.IsInf(beta[j], 0) {
			continue
		}
		coefficient := beta[j] / scale[j]
		tokens := 1e6 / coefficient
		if tokens <= 0 || math.IsInf(tokens, 0) || math.IsNaN(tokens) {
			continue
		}
		capacity := quotaFittedCapacity{tokens: tokens, low: 1e6 * scale[j] / (beta[j] + errorMargin), high: 1e6 * scale[j] / (beta[j] - errorMargin)}
		if math.IsInf(capacity.high, 0) || math.IsNaN(capacity.high) {
			continue
		}
		total := totals[name]
		usd := tokens / total.tokens * total.cost
		if !total.unpriced && usd >= 0 && !math.IsNaN(usd) && !math.IsInf(usd, 0) {
			capacity.cost = &usd
		}
		out[name] = capacity
	}
	return out
}

func quotaFitCholesky(g [][]float64) [][]float64 {
	l := make([][]float64, len(g))
	for i := range g {
		l[i] = make([]float64, len(g))
		for j := 0; j <= i; j++ {
			value := g[i][j]
			for k := 0; k < j; k++ {
				value -= l[i][k] * l[j][k]
			}
			if i == j {
				if value <= 1e-6 || math.IsNaN(value) || math.IsInf(value, 0) {
					return nil
				}
				l[i][j] = math.Sqrt(value)
			} else {
				l[i][j] = value / l[j][j]
			}
		}
	}
	return l
}

func quotaFitSolve(l [][]float64, rhs []float64) []float64 {
	v := make([]float64, len(rhs))
	for i := range v {
		value := rhs[i]
		for k := 0; k < i; k++ {
			value -= l[i][k] * v[k]
		}
		v[i] = value / l[i][i]
	}
	for i := len(v) - 1; i >= 0; i-- {
		value := v[i]
		for k := i + 1; k < len(v); k++ {
			value -= l[k][i] * v[k]
		}
		v[i] = value / l[i][i]
	}
	return v
}

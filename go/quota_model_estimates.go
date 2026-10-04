package main

import (
	"math"
	"sort"
	"time"
)

// A conservative one-percentage-point endpoint-difference error, even when
// raw upstream values expose more precision. Longer windows allow earlier,
// wider estimates; disconnected intervals still accumulate endpoint error.
const quotaModelQuantum = .01
const quotaModelMinDelta = .10

func quotaMinimumDelta(seconds int64) float64 {
	if seconds >= 7*86400 {
		return .03
	}
	return quotaModelMinDelta
}

type quotaModelInterval struct {
	start, end time.Time
	delta      float64
	model      string
	tokens     float64
	cost       float64
	unpriced   bool
	mixed      bool
	models     map[string]quotaIntervalModel
}

type quotaIntervalModel struct {
	tokens   float64
	cost     float64
	unpriced bool
}

// Only monotonic observations within one collection run can calibrate a model.
// Complete intervals from earlier runs remain usable after a restart.
// Hold the baseline through plateaus so rounded usage is not assigned to just
// the last request before the next increase. A decrease discards old estimates.
func quotaModelIntervals(p *quotaPeriod) []quotaModelInterval {
	var intervals []quotaModelInterval
	var base *quotaObservation
	for i := range p.Samples {
		sample := &p.Samples[i]
		if sample.ObservedAt.Before(p.Start) || sample.ObservedAt.After(p.End) {
			continue
		}
		if math.IsNaN(sample.Used) || math.IsInf(sample.Used, 0) || sample.Used < 0 || sample.Used > 1 {
			base = nil
			intervals = nil
			continue
		}
		epoch := sample.CollectionStartedAt
		if epoch.IsZero() {
			epoch = p.CollectionStartedAt
		}
		if epoch.IsZero() || sample.ObservedAt.Before(epoch) {
			base = nil
			continue
		}
		if base != nil {
			baseEpoch := base.CollectionStartedAt
			if baseEpoch.IsZero() {
				baseEpoch = p.CollectionStartedAt
			}
			if sample.Used < base.Used {
				intervals = nil
				base = nil
			} else if !epoch.Equal(baseEpoch) {
				// A restart prevents calibrating across the collection gap,
				// but does not invalidate complete historical intervals.
				base = nil
			}
		}
		if base == nil {
			base = sample
			continue
		}
		if !sample.ObservedAt.After(base.ObservedAt) {
			// Conflicting usage at one timestamp has no measurable interval.
			if sample.Used != base.Used {
				intervals = nil
			}
			base = sample
			continue
		}
		if sample.Used > base.Used {
			intervals = append(intervals, quotaModelInterval{start: base.ObservedAt, end: sample.ObservedAt, delta: sample.Used - base.Used})
			base = sample
		}
	}
	return intervals
}

// Preserve directly observed model capacities and supplement them with a joint
// fit across mixed intervals. Executions crossing observations cannot reliably
// attribute their token usage to an observed quota increment.
func applyQuotaModelEstimates(dto *quotaCycleDTO, w quotaWindow, p *quotaPeriod, facts []quotaFact, pricing *pricingSnapshot, now time.Time, current bool) {
	if dto == nil || p == nil || w.Unmapped {
		return
	}
	p, _ = quotaEffectivePeriod(p)
	intervals := quotaModelIntervals(p)
	if len(intervals) == 0 {
		return
	}
	// Mark ranges contaminated by crossing executions in O(log samples) per
	// fact rather than scanning every fact again for every observation.
	invalid := make([]int, len(intervals)+1)
	for _, f := range facts {
		if f.Provider != w.Provider || f.AuthIndex != w.AuthIndex || f.AuthID != w.AuthID || (w.Model != "" && f.Model != w.Model) || !f.Timestamp.Before(p.End) {
			continue
		}
		// A request that began in the previous period may still overlap this
		// period's first observation interval.
		if f.Timestamp.Before(p.Start) && (f.CompletedAt.IsZero() || !f.CompletedAt.After(p.Start)) {
			continue
		}
		tokens := detailTotalTokensForRequest(f.detail())
		if f.Failed && tokens == 0 && normalizedCacheTokens(f.Tokens) == 0 {
			continue
		}
		if f.TimestampSynthetic {
			invalid[0]++
			invalid[len(intervals)]--
			continue
		}
		first := sort.Search(len(intervals), func(i int) bool { return intervals[i].end.After(f.Timestamp) })
		if first == len(intervals) {
			continue
		}
		completed := f.CompletedAt
		if completed.IsZero() || completed.Before(f.Timestamp) {
			invalid[first]++
			invalid[len(intervals)]--
			continue
		}
		overlapEnd := completed
		if completed.Equal(f.Timestamp) {
			overlapEnd = completed.Add(time.Nanosecond)
		}
		last := sort.Search(len(intervals), func(i int) bool { return !intervals[i].start.Before(overlapEnd) })
		if last <= first {
			continue
		}
		if f.Model == "" {
			invalid[first]++
			invalid[last]--
			continue
		}
		interval := &intervals[first]
		if last != first+1 || f.Timestamp.Before(interval.start) || completed.After(interval.end) {
			invalid[first]++
			invalid[last]--
			continue
		}
		if interval.model != "" && interval.model != f.Model {
			interval.mixed = true
		}
		interval.model = f.Model
		interval.tokens += float64(tokens)
		cost := pricing.detailCost(f.Model, f.detail(), detailTotalsFromRequest(f.detail()))
		unpriced := !quotaPriceKnown(f, pricing)
		interval.cost = addNonNegativeCost(interval.cost, cost)
		interval.unpriced = interval.unpriced || unpriced
		if interval.models == nil {
			interval.models = make(map[string]quotaIntervalModel)
		}
		model := interval.models[f.Model]
		model.tokens += float64(tokens)
		model.cost = addNonNegativeCost(model.cost, cost)
		model.unpriced = model.unpriced || unpriced
		interval.models[f.Model] = model
	}
	type calibration struct {
		tokens, cost, delta, uncertainty float64
		end                              time.Time
		unpriced                         bool
	}
	minimumDelta := quotaMinimumDelta(w.Seconds)
	models := make(map[string]calibration)
	var eligible []quotaModelInterval
	bad := 0
	for i, interval := range intervals {
		bad += invalid[i]
		if bad > 0 || interval.tokens <= 0 {
			continue
		}
		if current && (interval.end.After(now) || now.Sub(interval.end) > time.Duration(w.Seconds)*time.Second/4) {
			continue
		}
		eligible = append(eligible, interval)
		if interval.mixed || interval.model == "" {
			continue
		}
		c := models[interval.model]
		if !c.end.Equal(interval.start) {
			c.uncertainty += quotaModelQuantum
		}
		c.end = interval.end
		c.tokens += interval.tokens
		c.cost = addNonNegativeCost(c.cost, interval.cost)
		c.delta += interval.delta
		c.unpriced = c.unpriced || interval.unpriced
		models[interval.model] = c
	}
	for i := range dto.ModelStats {
		row := &dto.ModelStats[i]
		c, ok := models[row.Model]
		if !ok || c.delta+1e-12 < minimumDelta || c.delta+1e-12 < minimumDelta/quotaModelQuantum*c.uncertainty {
			continue
		}
		tokens := c.tokens / c.delta
		if !math.IsNaN(tokens) && !math.IsInf(tokens, 0) && tokens > 0 {
			row.ModelOnlyTotalTokens = &tokens
			low, high := c.tokens/(c.delta+c.uncertainty), c.tokens/(c.delta-c.uncertainty)
			row.ModelOnlyTokensLow, row.ModelOnlyTokensHigh = &low, &high
		}
		cost := c.cost / c.delta
		low, high := c.cost/(c.delta+c.uncertainty), c.cost/(c.delta-c.uncertainty)
		if !c.unpriced && !math.IsNaN(cost) && !math.IsInf(cost, 0) && cost >= 0 && !math.IsInf(high, 0) {
			row.ModelOnlyTotalUSD = &cost
			row.ModelOnlyUSDLow, row.ModelOnlyUSDHigh = &low, &high
		}
	}
	fitted := fitQuotaMixedModels(eligible, minimumDelta)
	for i := range dto.ModelStats {
		row := &dto.ModelStats[i]
		if capacity, ok := fitted[row.Model]; ok && row.ModelOnlyTotalTokens == nil {
			tokens := capacity.tokens
			row.ModelOnlyTotalTokens, row.ModelOnlyTotalUSD = &tokens, capacity.cost
			row.ModelOnlyTokensLow, row.ModelOnlyTokensHigh = &capacity.low, &capacity.high
			if capacity.cost != nil {
				low, high := *capacity.cost*(capacity.low/tokens), *capacity.cost*(capacity.high/tokens)
				if math.IsInf(high, 0) || math.IsNaN(high) {
					row.ModelOnlyTotalUSD = nil
				} else {
					row.ModelOnlyUSDLow, row.ModelOnlyUSDHigh = &low, &high
				}
			}
		}
	}
}

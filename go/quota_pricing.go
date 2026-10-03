package main

import (
	"math"
	"sort"
	"time"
)

func (f quotaFact) detail() RequestDetail {
	return RequestDetail{Model: f.Model, Provider: f.Provider, Timestamp: f.Timestamp, TimestampSynthetic: f.TimestampSynthetic, Tokens: f.Tokens, Failed: f.Failed}
}

func quotaFactMatches(f quotaFact, w quotaWindow, p *quotaPeriod) bool {
	return p != nil && f.Provider == w.Provider && f.AuthIndex == w.AuthIndex && f.AuthID == w.AuthID &&
		!f.Timestamp.Before(p.Start) && f.Timestamp.Before(p.End) && (w.Model == "" || f.Model == w.Model)
}

func quotaPriceKnown(f quotaFact, pricing *pricingSnapshot) bool {
	if f.Tokens.TotalTokens == 0 {
		return true
	}
	if pricing == nil {
		return false
	}
	if _, ok := priceForDetailFromIndex(pricing.manualIndex, f.Model, f.Provider); ok {
		return true
	}
	_, ok := priceForDetailFromIndex(pricing.devIndex, f.Model, f.Provider)
	return ok
}

func quotaBuildPeriod(w quotaWindow, p *quotaPeriod, facts []quotaFact, pricing *pricingSnapshot) *quotaCycleDTO {
	if p == nil {
		return nil
	}
	v := &quotaCycleDTO{StartAt: p.Start, EndAt: p.End, ModelStats: []quotaModelStat{}}
	if len(p.Samples) > 0 {
		last := p.Samples[len(p.Samples)-1]
		percent := last.Used * 100
		v.UsedPercent, v.ObservedAt = &percent, last.ObservedAt
	}
	if w.Unmapped {
		v.ModelStats = nil
		return v
	}
	acc := newRangeAPIDetailAccumulator()
	costs := make(map[string]float64)
	unpriced := make(map[string]bool)
	for _, f := range facts {
		if !quotaFactMatches(f, w, p) {
			continue
		}
		d := f.detail()
		acc.add(f.Model, d, nil, false)
		if !quotaPriceKnown(f, pricing) {
			unpriced[f.Model] = true
		}
		costs[f.Model] = addNonNegativeCost(costs[f.Model], pricing.detailCost(f.Model, d, detailTotalsFromRequest(d)))
	}
	for model, stat := range acc.modelAgg {
		stat.EstimatedCost = costs[model]
		acc.summary.EstimatedCost = addNonNegativeCost(acc.summary.EstimatedCost, costs[model])
		row := quotaModelStat{ModelStat: finalizeModelStat(*stat)}
		if !unpriced[model] {
			cost := costs[model]
			row.CostUSD = &cost
		}
		v.ModelStats = append(v.ModelStats, row)
	}
	sort.Slice(v.ModelStats, func(i, j int) bool {
		if v.ModelStats[i].TotalRequests != v.ModelStats[j].TotalRequests {
			return v.ModelStats[i].TotalRequests > v.ModelStats[j].TotalRequests
		}
		return v.ModelStats[i].Model < v.ModelStats[j].Model
	})
	if acc.summary.TotalRequests > 0 {
		v.Summary = &quotaUsageSummary{APIDetailSummary: acc.summary}
		if len(unpriced) == 0 {
			v.Summary.CostUSD = &v.Summary.APIDetailSummary.EstimatedCost
		}
	}
	return v
}

func quotaCostAt(w quotaWindow, p *quotaPeriod, facts []quotaFact, pricing *pricingSnapshot, at time.Time) float64 {
	var sum float64
	for _, f := range facts {
		if quotaFactMatches(f, w, p) && !f.Timestamp.After(at) && !f.CompletedAt.After(at) {
			d := f.detail()
			sum = addNonNegativeCost(sum, pricing.detailCost(f.Model, d, detailTotalsFromRequest(d)))
		}
	}
	return sum
}

func quotaEstimate(w quotaWindow, p *quotaPeriod, facts []quotaFact, pricing *pricingSnapshot, started, now time.Time) (*float64, *float64) {
	if p == nil || w.Unmapped || len(p.Samples) == 0 || !now.Before(p.End) {
		return nil, nil
	}
	last := p.Samples[len(p.Samples)-1]
	// Keep displayed watermarks, but do not extend an old calibration indefinitely.
	if last.ObservedAt.Before(started) || last.ObservedAt.After(now) || now.Sub(last.ObservedAt) > time.Duration(w.Seconds)*time.Second/4 {
		return nil, nil
	}
	return quotaEstimateFromSamples(w, p, facts, pricing, started)
}

// Completed periods use their final observed watermark and historical baseline,
// without the live estimate's freshness deadline or a later process's restart.
func quotaEstimateFromSamples(w quotaWindow, p *quotaPeriod, facts []quotaFact, pricing *pricingSnapshot, started time.Time) (*float64, *float64) {
	if p == nil || w.Unmapped || len(p.Samples) == 0 || started.IsZero() {
		return nil, nil
	}
	last := p.Samples[len(p.Samples)-1]
	if last.ObservedAt.Before(started) {
		return nil, nil
	}
	matched := false
	for _, f := range facts {
		if !quotaFactMatches(f, w, p) || f.Timestamp.After(last.ObservedAt) || f.CompletedAt.After(last.ObservedAt) {
			continue
		}
		matched = true
		if !quotaPriceKnown(f, pricing) {
			return nil, nil
		}
	}
	if !matched {
		return nil, nil
	}
	used, cost := last.Used, quotaCostAt(w, p, facts, pricing, last.ObservedAt)
	baseline := 0
	for baseline < len(p.Samples) && p.Samples[baseline].ObservedAt.Before(started) {
		baseline++
	}
	dropped := false
	for i := baseline + 1; i < len(p.Samples); i++ {
		if p.Samples[i].Used < p.Samples[i-1].Used {
			baseline, dropped = i, true
		}
	}
	if started.After(p.Start) || dropped {
		if baseline >= len(p.Samples)-1 {
			return nil, nil
		}
		first := p.Samples[baseline]
		if !first.ObservedAt.Before(last.ObservedAt) {
			return nil, nil
		}
		hasIncrement := false
		for _, f := range facts {
			completed := f.CompletedAt
			if completed.IsZero() {
				completed = f.Timestamp
			}
			if quotaFactMatches(f, w, p) && completed.After(first.ObservedAt) && !completed.After(last.ObservedAt) {
				hasIncrement = true
				break
			}
		}
		if !hasIncrement {
			return nil, nil
		}
		used -= first.Used
		cost -= quotaCostAt(w, p, facts, pricing, first.ObservedAt)
	}
	if used <= 0 || cost < 0 {
		return nil, nil
	}
	total := cost / used
	remaining := total * (1 - last.Used)
	if math.IsInf(total, 0) || math.IsNaN(total) {
		return nil, nil
	}
	return &total, &remaining
}

func (s *RequestStatistics) quotaCyclesForAPILocked(api string, now time.Time) []quotaCredentialDTO {
	if s.quota == nil {
		return nil
	}
	s.pruneQuotaLocked(now)
	credentials := make(map[string]bool)
	for _, f := range s.quota.Facts {
		if f.API == api {
			credentials[quotaCredentialKey(f.Provider, f.AuthIndex, f.AuthID)] = true
		}
	}
	factsByCredential := make(map[string][]quotaFact, len(credentials))
	for _, f := range s.quota.Facts {
		key := quotaCredentialKey(f.Provider, f.AuthIndex, f.AuthID)
		if credentials[key] {
			factsByCredential[key] = append(factsByCredential[key], f)
		}
	}
	pricing := s.pricingSnapshotLocked()
	byCredential := make(map[string]*quotaCredentialDTO)
	for _, w := range s.quota.Windows {
		key := quotaCredentialKey(w.Provider, w.AuthIndex, w.AuthID)
		if !credentials[key] || w.Hidden {
			continue
		}
		facts := factsByCredential[key]
		v := byCredential[key]
		if v == nil {
			v = &quotaCredentialDTO{Provider: w.Provider, AuthIndex: w.AuthIndex, CredentialName: w.AuthID}
			byCredential[key] = v
		}
		group := quotaGroupDTO{GroupID: w.Group + ":" + w.Slot, Name: w.Name, WindowSeconds: w.Seconds}
		if current := quotaBuildPeriod(w, w.Current, facts, pricing); current != nil {
			total, remaining := quotaEstimate(w, w.Current, facts, pricing, s.quota.StartedAt, now)
			current.EstimatedTotalUSD = total
			group.Current = &quotaCurrentDTO{quotaCycleDTO: *current, EstimatedRemainingUSD: remaining}
		}
		group.Previous = quotaBuildPeriod(w, w.Previous, facts, pricing)
		if previous := group.Previous; previous != nil && previous.UsedPercent != nil {
			if *previous.UsedPercent == 100 {
				if previous.Summary != nil {
					previous.ActualTotalUSD = previous.Summary.CostUSD
				}
			} else {
				started := w.Previous.CollectionStartedAt
				if started.IsZero() {
					started = s.quota.StartedAt
				}
				previous.EstimatedTotalUSD, _ = quotaEstimateFromSamples(w, w.Previous, facts, pricing, started)
			}
		}
		v.Groups = append(v.Groups, group)
	}
	var out []quotaCredentialDTO
	for _, v := range byCredential {
		sort.Slice(v.Groups, func(i, j int) bool {
			if v.Groups[i].WindowSeconds != v.Groups[j].WindowSeconds {
				return v.Groups[i].WindowSeconds < v.Groups[j].WindowSeconds
			}
			return v.Groups[i].GroupID < v.Groups[j].GroupID
		})
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Provider+out[i].AuthIndex+out[i].CredentialName < out[j].Provider+out[j].AuthIndex+out[j].CredentialName
	})
	return out
}

// Keep quota detail discoverable after main-ledger retention or filtering has
// removed an API from the ordinary usage summary.
func (s *RequestStatistics) quotaAPINamesLocked() []string {
	if s.quota == nil {
		return nil
	}
	credentials := make(map[string]bool)
	for _, w := range s.quota.Windows {
		if !w.Hidden && (w.Current != nil || w.Previous != nil) {
			credentials[quotaCredentialKey(w.Provider, w.AuthIndex, w.AuthID)] = true
		}
	}
	apis := make(map[string]bool)
	for _, f := range s.quota.Facts {
		if f.API != "" && credentials[quotaCredentialKey(f.Provider, f.AuthIndex, f.AuthID)] {
			apis[f.API] = true
		}
	}
	var names []string
	for api := range apis {
		names = append(names, api)
	}
	sort.Strings(names)
	return names
}

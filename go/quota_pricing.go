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
	if detailTotalTokensForRequest(f.detail()) == 0 && normalizedCacheTokens(f.Tokens) == 0 {
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

// Use the first observation after the latest usage decrease as a conservative
// reset boundary. The exact reset time is not supplied; never reuse old spend.
func quotaEffectivePeriod(p *quotaPeriod) (*quotaPeriod, *float64) {
	if p == nil {
		return nil, nil
	}
	index := -1
	for i := 1; i < len(p.Samples); i++ {
		if p.Samples[i].Used < p.Samples[i-1].Used && p.Samples[i].ObservedAt.Before(p.End) {
			index = i
		}
	}
	if index < 0 {
		return p, nil
	}
	copy := *p
	copy.Start = p.Samples[index].ObservedAt
	copy.Samples = p.Samples[index:]
	used := copy.Samples[0].Used * 100
	return &copy, &used
}

func quotaBuildPeriod(w quotaWindow, p *quotaPeriod, facts []quotaFact, pricing *pricingSnapshot) *quotaCycleDTO {
	if p == nil {
		return nil
	}
	p, resetUsed := quotaEffectivePeriod(p)
	v := &quotaCycleDTO{WindowSeconds: w.Seconds, ResetBaselineUsedPercent: resetUsed, StartAt: p.Start, EndAt: p.End, ModelStats: []quotaModelStat{}, Unmapped: w.Unmapped}
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
			if v.UsedPercent != nil && *v.UsedPercent == 100 {
				// Exhaustion is a recorded result, not another calibration
				// sample. Use the same tracked spend for current and previous
				// periods, including late usage and costs after the watermark.
				v.ActualTotalUSD = v.Summary.CostUSD
			}
		}
	}
	return v
}

// Both operands come from the displayed period. A process restart or a missing
// early observation must not silently switch this to a marginal-cost estimate.
func quotaEstimate(period *quotaCycleDTO) (*float64, *float64) {
	if period == nil || period.Summary == nil || period.Summary.CostUSD == nil || period.UsedPercent == nil {
		return nil, nil
	}
	used, cost := *period.UsedPercent/100, *period.Summary.CostUSD
	if used <= 0 || used > 1 || cost < 0 || math.IsNaN(used) || math.IsInf(used, 0) {
		return nil, nil
	}
	consumed := used
	if period.ResetBaselineUsedPercent != nil {
		consumed -= *period.ResetBaselineUsedPercent / 100
		if consumed < quotaMinimumDelta(period.WindowSeconds)-1e-12 {
			return nil, nil
		}
	}
	total := cost / consumed
	remaining := total * (1 - used)
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
	// Older snapshots can contain Antigravity requests without quota facts.
	for _, ref := range s.antigravityQuotaCredentialsForAPILocked(api) {
		credentials[quotaCredentialKey(ref.Provider, ref.AuthIndex, ref.AuthID)] = true
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
			applyQuotaModelEstimates(current, w, w.Current, facts, pricing, now, true)
			var remaining *float64
			if current.UsedPercent != nil && *current.UsedPercent == 100 {
				if !w.Unmapped {
					zero := 0.0
					remaining = &zero
				}
			} else if !current.ObservedAt.After(now) && now.Sub(current.ObservedAt) <= time.Duration(w.Seconds)*time.Second/4 {
				current.EstimatedTotalUSD, remaining = quotaEstimate(current)
			}
			group.Current = &quotaCurrentDTO{quotaCycleDTO: *current, EstimatedRemainingUSD: remaining}
		}
		group.Previous = quotaBuildPeriod(w, w.Previous, facts, pricing)
		applyQuotaModelEstimates(group.Previous, w, w.Previous, facts, pricing, now, false)
		if previous := group.Previous; previous != nil && previous.UsedPercent != nil && *previous.UsedPercent != 100 {
			previous.EstimatedTotalUSD, _ = quotaEstimate(previous)
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

// Include identities before the first Antigravity quota query, so the dashboard
// only probes credentials that actually belong to the selected upstream API.
func (s *RequestStatistics) antigravityQuotaCredentialsForAPILocked(api string) []quotaCredentialRef {
	refs := make(map[string]quotaCredentialRef)
	add := func(provider, index, id string) {
		if provider == "antigravity" && index != "" && id != "" {
			refs[quotaCredentialKey(provider, index, id)] = quotaCredentialRef{Provider: provider, AuthIndex: index, AuthID: id}
		}
	}
	if s.quota != nil {
		for _, f := range s.quota.Facts {
			if f.API == api {
				add(f.Provider, f.AuthIndex, f.AuthID)
			}
		}
	}
	// Quota tracking was introduced after ordinary accounting. Discover retained
	// historical identities without inventing quota observations or duplicating facts.
	if a := s.apis[api]; a != nil {
		for _, m := range a.Models {
			if m == nil {
				continue
			}
			for i := 0; i < m.accountingCount(); i++ {
				d := m.accountingDetailAt(i)
				if quotaEligible(d) {
					add(d.Provider, d.AuthIndex, d.AuthID)
				}
			}
		}
	}
	var out []quotaCredentialRef
	for _, ref := range refs {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AuthIndex < out[j].AuthIndex })
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

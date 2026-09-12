package main

import (
	"cmp"
	"slices"
	"strings"
)

// Most client/model combinations use one provider. A temporary range query
// needs a single counter slice, not a map, map buckets, individual pointers
// and a second finalized copy for each of thousands of combinations. Bound
// linear lookup to eight providers, then use the existing map implementation.
func incrementRangeClientProviders(model *ClientAPIModelStat, provider string, failed bool, totals detailTotals) {
	if model.providerStats != nil {
		model.providerStats = incrementModelProviderStats(model.providerStats, provider, failed, totals)
		return
	}
	// Range scans walk API/model groups, so consecutive rows commonly use
	// the provider most recently added to this model. Avoid normalization
	// and a linear search on that hot path; mismatches use the exact legacy
	// lowercase-and-trim identity rules below.
	if last := len(model.Providers) - 1; last >= 0 && model.Providers[last].Provider == provider {
		incrementModelProviderStat(&model.Providers[last], failed, totals)
		return
	}
	key := modelProviderStatsKey(provider)
	for i := len(model.Providers) - 1; i >= 0; i-- {
		if modelProviderStatsKey(model.Providers[i].Provider) == key {
			incrementModelProviderStat(&model.Providers[i], failed, totals)
			return
		}
	}
	if len(model.Providers) == 8 {
		model.providerStats = make(map[string]*ModelProviderStat, len(model.Providers)+1)
		for _, value := range model.Providers {
			model.providerStats[modelProviderStatsKey(value.Provider)] = new(value)
		}
		model.Providers = nil
		model.providerStats = incrementModelProviderStats(model.providerStats, provider, failed, totals)
		return
	}
	model.Providers = append(model.Providers, ModelProviderStat{Provider: strings.TrimSpace(provider)})
	incrementModelProviderStat(&model.Providers[len(model.Providers)-1], failed, totals)
}

func sortRangeModelProviders(providers []ModelProviderStat) {
	slices.SortFunc(providers, func(a, b ModelProviderStat) int {
		if order := cmp.Compare(b.TotalRequests, a.TotalRequests); order != 0 {
			return order
		}
		return strings.Compare(a.Provider, b.Provider)
	})
}

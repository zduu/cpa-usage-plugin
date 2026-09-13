package main

// Snapshot accounting is not the same as replaying requests. These bounded
// reducers retain the original aggregate basis as well as the surviving
// detail totals; a residual is never represented by a fabricated request.

import "strings"

type sqliteSnapshotTotals struct {
	TotalRequests, SuccessCount, FailureCount int64
	TotalTokens, InputTokens, OutputTokens    int64
	CachedTokens, CacheWriteTokens            int64
	ReasoningTokens, LatencySum, LatencyN     int64
}

func sqliteSnapshotTotalsFromModel(m *modelStats) sqliteSnapshotTotals {
	if m == nil {
		return sqliteSnapshotTotals{}
	}
	return sqliteSnapshotTotals{m.TotalRequests, m.SuccessCount, m.FailureCount,
		m.TotalTokens, m.InputTokens, m.OutputTokens, m.CachedTokens,
		m.CacheWriteTokens, m.ReasoningTokens, m.latencySum, m.latencyN}
}

func sqliteSnapshotTotalsFromRaw(m ModelSnapshot) sqliteSnapshotTotals {
	latencySum, latencyN := restoredLatencyAggregate(m.AvgLatencyMs, nonNegativeInt64(m.TotalRequests))
	return sqliteSnapshotTotals{nonNegativeInt64(m.TotalRequests), nonNegativeInt64(m.SuccessCount), nonNegativeInt64(m.FailureCount),
		nonNegativeInt64(m.TotalTokens), nonNegativeInt64(m.InputTokens), nonNegativeInt64(m.OutputTokens), nonNegativeInt64(m.CachedTokens),
		nonNegativeInt64(m.CacheWriteTokens), nonNegativeInt64(m.ReasoningTokens), latencySum, latencyN}
}

func (t sqliteSnapshotTotals) legacy() storageSnapshotDetailAggregate {
	return storageSnapshotDetailAggregate{t.TotalRequests, t.SuccessCount, t.FailureCount,
		t.TotalTokens, t.InputTokens, t.OutputTokens, t.CachedTokens,
		t.CacheWriteTokens, t.ReasoningTokens, t.LatencySum, t.LatencyN}
}

func (t *sqliteSnapshotTotals) add(other sqliteSnapshotTotals) {
	t.TotalRequests = addNonNegativeInt64(t.TotalRequests, other.TotalRequests)
	t.SuccessCount = addNonNegativeInt64(t.SuccessCount, other.SuccessCount)
	t.FailureCount = addNonNegativeInt64(t.FailureCount, other.FailureCount)
	t.TotalTokens = addNonNegativeInt64(t.TotalTokens, other.TotalTokens)
	t.InputTokens = addNonNegativeInt64(t.InputTokens, other.InputTokens)
	t.OutputTokens = addNonNegativeInt64(t.OutputTokens, other.OutputTokens)
	t.CachedTokens = addNonNegativeInt64(t.CachedTokens, other.CachedTokens)
	t.CacheWriteTokens = addNonNegativeInt64(t.CacheWriteTokens, other.CacheWriteTokens)
	t.ReasoningTokens = addNonNegativeInt64(t.ReasoningTokens, other.ReasoningTokens)
	t.LatencySum = addNonNegativeInt64(t.LatencySum, other.LatencySum)
	t.LatencyN = addNonNegativeInt64(t.LatencyN, other.LatencyN)
}

func (t *sqliteSnapshotTotals) addDetail(detail RequestDetail, totals detailTotals) {
	count := sqliteSnapshotTotals{TotalRequests: 1, TotalTokens: totals.totalTokens,
		InputTokens: totals.inputTokens, OutputTokens: totals.outputTokens,
		CachedTokens: totals.cachedTokens, CacheWriteTokens: totals.cacheWriteTokens,
		ReasoningTokens: totals.reasoningTokens, LatencySum: totals.latencySum, LatencyN: totals.latencyN}
	if detail.Failed {
		count.FailureCount = 1
	} else {
		count.SuccessCount = 1
	}
	t.add(count)
}

// The legacy non-split path fills token components and missing latency only.
// It deliberately does NOT replace request counts or TotalTokens with detail
// totals, even if those totals exceed the snapshot's aggregate basis.
func (t *sqliteSnapshotTotals) fillComponents(other sqliteSnapshotTotals) {
	t.InputTokens = maxInt64(t.InputTokens, other.InputTokens)
	t.OutputTokens = maxInt64(t.OutputTokens, other.OutputTokens)
	t.CachedTokens = maxInt64(t.CachedTokens, other.CachedTokens)
	t.CacheWriteTokens = maxInt64(t.CacheWriteTokens, other.CacheWriteTokens)
	t.ReasoningTokens = maxInt64(t.ReasoningTokens, other.ReasoningTokens)
	if t.LatencyN == 0 && other.LatencyN > 0 {
		t.LatencySum, t.LatencyN = other.LatencySum, other.LatencyN
	}
}

type sqliteSnapshotFirstDetail struct {
	Set      bool
	Archived bool
	Position int64
	API      string
}

func (f *sqliteSnapshotFirstDetail) consider(api string, archived bool, position int64) {
	// Projection object keys are lexical (accounting precedes details), but
	// legacy accountingDetails always visits visible details first.
	if !f.Set || (!archived && f.Archived) || (archived == f.Archived && position < f.Position) {
		*f = sqliteSnapshotFirstDetail{true, archived, position, api}
	}
}

// One original API/model group, not a map of all normalized destination
// groups. Split and residual routing is intentionally explicit for the next
// coordinator phase. Counts are provisional until protocol repair is done.
type sqliteMigrationSnapshotGroup struct {
	Node, Parent                 int64
	Scope, Name                  string
	Ignored                      bool
	Closed                       bool
	Snapshot                     ModelSnapshot // Scalar fields only; never holds slices/maps.
	Details                      sqliteSnapshotTotals
	Children                     sqliteSnapshotTotals
	SplitChildren                sqliteSnapshotTotals
	Restored                     sqliteSnapshotTotals
	Split                        sqliteSnapshotTotals
	Residual                     *sqliteSnapshotTotals
	FirstAPI, FirstResidualAPI   sqliteSnapshotFirstDetail
	SplitAPI, SplitModel         bool
	HasProviderSnapshot          bool
	Visible, Archived, Fallbacks int64
	// The legacy zero-total branch discards ALL aggregate residuals and
	// rebuilds from details, including the daily/hourly series.
	RebuildFromDetails              bool
	CatalogGroups, CatalogProviders int64
	// Old recovery ranges over a Go map here. A deterministic candidate is
	// useful for analysis, but differing possible residual destinations must
	// not silently become an activation-time compatibility decision.
	AmbiguousResidualAPI bool
}

func (g *sqliteMigrationSnapshotGroup) finish(version int) {
	m := g.Snapshot
	if version < currentStorageSnapshotVersion {
		m.CachedTokens = legacyCacheReadTokens(m.CachedTokens, m.CacheWriteTokens)
	}
	g.Restored = sqliteSnapshotTotalsFromRaw(m)
	switch g.Scope {
	case "model":
		g.Restored.fillComponents(g.Details)
		g.Split = g.Details
		if residual := storageSnapshotResidualModelStats(m, g.Details.legacy()); residual != nil {
			value := sqliteSnapshotTotalsFromModel(residual)
			g.Residual = &value
			g.Split.add(value)
		}
		if g.SplitModel {
			g.Restored = g.Split
		}
	case "api":
		g.Restored.fillComponents(g.Children)
		g.Split = g.SplitChildren
		if g.SplitAPI {
			g.Restored = g.Split
		}
		g.AmbiguousResidualAPI = g.SplitAPI && g.AmbiguousResidualAPI
		if !g.FirstResidualAPI.Set {
			g.FirstResidualAPI = sqliteSnapshotFirstDetail{Set: true, API: storageSnapshotResidualAPIName(g.Name, APISnapshot{})}
		}
	case "usage":
		g.Restored.fillComponents(g.Children)
		if g.Restored.TotalRequests == 0 && g.Details.TotalRequests > 0 {
			g.Restored = g.Details
			g.RebuildFromDetails = true
		}
	}
	if g.Ignored {
		g.Restored, g.Split, g.Residual = sqliteSnapshotTotals{}, sqliteSnapshotTotals{}, nil
	}
	g.Closed = true
}

func (g *sqliteMigrationSnapshotGroup) addChild(child *sqliteMigrationSnapshotGroup) {
	g.Fallbacks = addNonNegativeInt64(g.Fallbacks, child.Fallbacks)
	g.CatalogGroups = addNonNegativeInt64(g.CatalogGroups, child.CatalogGroups)
	g.CatalogProviders = addNonNegativeInt64(g.CatalogProviders, child.CatalogProviders)
	g.AmbiguousResidualAPI = g.AmbiguousResidualAPI || child.AmbiguousResidualAPI
	if child.Ignored {
		return
	}
	g.Children.add(child.Restored)
	g.SplitChildren.add(child.Split)
	g.Details.add(child.Details)
	g.Visible = addNonNegativeInt64(g.Visible, child.Visible)
	g.Archived = addNonNegativeInt64(g.Archived, child.Archived)
	if g.Scope == "api" {
		g.SplitAPI = g.SplitAPI || child.SplitAPI || (g.FirstAPI.Set && child.FirstAPI.Set && g.FirstAPI.API != child.FirstAPI.API)
		if !g.FirstAPI.Set && child.FirstAPI.Set {
			g.FirstAPI = child.FirstAPI
		}
		if g.FirstResidualAPI.Set && child.FirstResidualAPI.Set && g.FirstResidualAPI.API != child.FirstResidualAPI.API {
			g.AmbiguousResidualAPI = true
		}
		if !g.FirstResidualAPI.Set && child.FirstResidualAPI.Set {
			g.FirstResidualAPI = child.FirstResidualAPI
		}
	}
}

type sqliteMigrationSnapshotProvider struct {
	Key         string
	Snapshot    ModelProviderStat
	Details     ModelProviderStat
	FirstDetail sqliteSnapshotFirstDetail
}

func (p sqliteMigrationSnapshotProvider) residual() *ModelProviderStat {
	key := modelProviderStatsKey(p.Snapshot.Provider)
	used := map[string]*ModelProviderStat{key: &p.Details}
	return residualModelProviderStats([]ModelProviderStat{p.Snapshot}, used)[key]
}

func sqliteSnapshotGroupName(scope, name string) string {
	if scope == "model" {
		return normalizeModelName(name)
	}
	return strings.TrimSpace(name)
}

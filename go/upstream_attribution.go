package main

import (
	"strings"
	"sync"
	"time"
)

const defaultUpstreamAttributionDelay = 90 * time.Second

const (
	maxUpstreamAttributionRoutes   = 4096
	maxPendingUpstreamAttribution  = 4096
	maxHistoricalAttributionRoutes = 65536
)

var (
	upstreamAttributionDelay = defaultUpstreamAttributionDelay
	upstreamAttributions     = newUpstreamAttributionCoordinator()
)

// upstreamAttributionCoordinator handles a CPA compatibility edge case where
// a successful DeepSeek request arrives as native usage with a claude:apikey
// identity even though the same client route is served by a named
// OpenAI-compatible upstream. The Claude-shaped record is only rewritten when
// a unique provider-specific OpenAI-compatible identity corroborates it.
type upstreamAttributionCoordinator struct {
	mu      sync.Mutex
	routes  *upstreamRouteResolver
	pending map[*pendingUpstreamAttribution]struct{}
	closed  bool
}

type pendingUpstreamAttribution struct {
	record    UsageRecord
	commit    func(UsageRecord)
	timer     *time.Timer
	cancelled bool
}

type resolvedUpstreamAttribution struct {
	pending *pendingUpstreamAttribution
	route   upstreamRouteIdentity
}

type upstreamRouteIdentity struct {
	Provider  string
	Source    string
	AuthID    string
	AuthIndex string
	AuthType  string
	BaseURL   string
}

type upstreamRoute struct {
	identity  upstreamRouteIdentity
	ambiguous bool
}

type upstreamRouteResolver struct {
	routes    map[string]upstreamRoute
	maxRoutes int
}

func newUpstreamAttributionCoordinator() *upstreamAttributionCoordinator {
	return &upstreamAttributionCoordinator{
		routes:  newUpstreamRouteResolver(maxUpstreamAttributionRoutes),
		pending: make(map[*pendingUpstreamAttribution]struct{}),
	}
}

func newUpstreamRouteResolver(maxRoutes int) *upstreamRouteResolver {
	return &upstreamRouteResolver{routes: make(map[string]upstreamRoute), maxRoutes: maxRoutes}
}

// Submit commits ordinary records immediately. Suspicious Claude-attributed
// DeepSeek records wait briefly for corroboration. The return value reports
// whether a new trusted route was learned, allowing callers to reconcile a
// previously timed-out in-memory detail once per newly observed route.
func (c *upstreamAttributionCoordinator) Submit(record UsageRecord, commit func(UsageRecord)) bool {
	if commit == nil {
		return false
	}
	if c == nil {
		commit(record)
		return false
	}

	if isTrustedOpenAICompatibleAttribution(record) {
		var resolved []resolvedUpstreamAttribution
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			commit(record)
			return false
		}
		learned := c.routes.learnRecord(record)
		for item := range c.pending {
			if item == nil || item.cancelled {
				continue
			}
			route, ok := c.routes.lookupRecord(item.record)
			if !ok {
				continue
			}
			item.cancelled = true
			if item.timer != nil {
				item.timer.Stop()
			}
			delete(c.pending, item)
			resolved = append(resolved, resolvedUpstreamAttribution{pending: item, route: route})
		}
		c.mu.Unlock()

		for _, item := range resolved {
			item.pending.commit(applyUpstreamRoute(item.pending.record, item.route))
		}
		commit(record)
		return learned
	}

	if !isSuspiciousClaudeDeepSeekAttribution(record) || len(upstreamAttributionRecordLookupKeys(record)) == 0 {
		commit(record)
		return false
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		commit(record)
		return false
	}
	if route, ok := c.routes.lookupRecord(record); ok {
		c.mu.Unlock()
		commit(applyUpstreamRoute(record, route))
		return false
	}
	if len(c.pending) >= maxPendingUpstreamAttribution {
		c.mu.Unlock()
		commit(record)
		return false
	}

	delay := upstreamAttributionDelay
	if delay < 0 {
		delay = 0
	}
	item := &pendingUpstreamAttribution{record: record, commit: commit}
	c.pending[item] = struct{}{}
	item.timer = time.AfterFunc(delay, func() { c.commitPending(item) })
	c.mu.Unlock()
	return false
}

func (c *upstreamAttributionCoordinator) Flush() {
	if c == nil {
		return
	}
	for _, item := range c.detachPending(true) {
		item.commit(item.record)
	}
}

// EnrichPending preserves response-only metadata when native usage reached the
// fallback coordinator before the response interceptor, but attribution is
// still waiting for corroboration and has not committed the native record to
// statistics yet.
func (c *upstreamAttributionCoordinator) EnrichPending(record UsageRecord, enrichment UsageRecord) bool {
	if c == nil {
		return false
	}
	targetKey := usageRecordFingerprint(record)
	if targetKey == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var selected *pendingUpstreamAttribution
	var selectedDistance time.Duration
	for item := range c.pending {
		if item == nil || item.cancelled || usageRecordFingerprint(item.record) != targetKey {
			continue
		}
		distance := item.record.RequestedAt.Sub(record.RequestedAt)
		if distance < 0 {
			distance = -distance
		}
		if selected == nil || distance < selectedDistance {
			selected = item
			selectedDistance = distance
		}
	}
	if selected == nil {
		return false
	}
	selected.record = enrichUsageRecord(selected.record, enrichment)
	return true
}

func (c *upstreamAttributionCoordinator) Reset() {
	if c == nil {
		return
	}
	for _, item := range c.detachPending(false) {
		item.commit(item.record)
	}
}

func (c *upstreamAttributionCoordinator) detachPending(closeCoordinator bool) []*pendingUpstreamAttribution {
	c.mu.Lock()
	c.closed = closeCoordinator
	pending := make([]*pendingUpstreamAttribution, 0, len(c.pending))
	for item := range c.pending {
		if item == nil || item.cancelled {
			continue
		}
		item.cancelled = true
		if item.timer != nil {
			item.timer.Stop()
		}
		pending = append(pending, item)
	}
	c.pending = make(map[*pendingUpstreamAttribution]struct{})
	c.routes = newUpstreamRouteResolver(maxUpstreamAttributionRoutes)
	c.mu.Unlock()
	return pending
}

func (c *upstreamAttributionCoordinator) commitPending(item *pendingUpstreamAttribution) {
	if c == nil || item == nil {
		return
	}
	c.mu.Lock()
	if c.closed || item.cancelled {
		delete(c.pending, item)
		c.mu.Unlock()
		return
	}
	item.cancelled = true
	delete(c.pending, item)
	route, resolved := c.routes.lookupRecord(item.record)
	c.mu.Unlock()
	if resolved {
		item.commit(applyUpstreamRoute(item.record, route))
		return
	}
	item.commit(item.record)
}

func (r *upstreamRouteResolver) learnRecord(record UsageRecord) bool {
	if r == nil || !isTrustedOpenAICompatibleAttribution(record) {
		return false
	}
	identity := upstreamRouteIdentityFromRecord(record)
	return r.learn(upstreamAttributionRecordLearnKeys(record), identity)
}

func (r *upstreamRouteResolver) learnDetail(detail RequestDetail) bool {
	if r == nil || !isTrustedOpenAICompatibleDetail(detail) {
		return false
	}
	identity := upstreamRouteIdentityFromDetail(detail)
	return r.learn(upstreamAttributionDetailLearnKeys(detail), identity)
}

func (r *upstreamRouteResolver) learn(keys []string, identity upstreamRouteIdentity) bool {
	learned := false
	for _, key := range keys {
		if key == "" {
			continue
		}
		existing, ok := r.routes[key]
		if !ok {
			if r.maxRoutes > 0 && len(r.routes) >= r.maxRoutes {
				continue
			}
			r.routes[key] = upstreamRoute{identity: identity}
			learned = true
			continue
		}
		if existing.ambiguous || sameUpstreamRoute(existing.identity, identity) {
			continue
		}
		existing.ambiguous = true
		r.routes[key] = existing
	}
	return learned
}

func (r *upstreamRouteResolver) lookupRecord(record UsageRecord) (upstreamRouteIdentity, bool) {
	if r == nil {
		return upstreamRouteIdentity{}, false
	}
	return r.lookup(upstreamAttributionRecordLookupKeys(record))
}

func (r *upstreamRouteResolver) lookupDetail(detail RequestDetail) (upstreamRouteIdentity, bool) {
	if r == nil {
		return upstreamRouteIdentity{}, false
	}
	return r.lookup(upstreamAttributionDetailLookupKeys(detail))
}

func (r *upstreamRouteResolver) lookup(keys []string) (upstreamRouteIdentity, bool) {
	for _, key := range keys {
		route, ok := r.routes[key]
		if ok && !route.ambiguous {
			return route.identity, true
		}
	}
	return upstreamRouteIdentity{}, false
}

func isTrustedOpenAICompatibleAttribution(record UsageRecord) bool {
	if record.Failed || !usageDetailHasTokens(record.Detail) {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(record.Provider))
	if !strings.HasPrefix(provider, "openai-compatible-") || provider == "openai-compatible" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(record.AuthID)), "openai-compatibility:") || strings.TrimSpace(record.AuthIndex) == "" {
		return false
	}
	return strings.HasPrefix(upstreamAttributionRecordModel(record), "deepseek-")
}

func isSuspiciousClaudeDeepSeekAttribution(record UsageRecord) bool {
	if record.Failed || !usageDetailHasTokens(record.Detail) || usageProviderFamily(record.Provider) != "claude" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(record.AuthID)), "claude:apikey:") {
		return false
	}
	return strings.HasPrefix(upstreamAttributionRecordModel(record), "deepseek-")
}

func isTrustedOpenAICompatibleDetail(detail RequestDetail) bool {
	if detail.Failed || !requestDetailHasTokens(detail) {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(detail.Provider))
	if !strings.HasPrefix(provider, "openai-compatible-") || provider == "openai-compatible" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(detail.AuthID)), "openai-compatibility:") || strings.TrimSpace(detail.AuthIndex) == "" {
		return false
	}
	return strings.HasPrefix(upstreamAttributionDetailModel(detail), "deepseek-")
}

func isSuspiciousClaudeDeepSeekDetail(detail RequestDetail) bool {
	if detail.Failed || !requestDetailHasTokens(detail) || usageProviderFamily(detail.Provider) != "claude" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(detail.AuthID)), "claude:apikey:") {
		return false
	}
	return strings.HasPrefix(upstreamAttributionDetailModel(detail), "deepseek-")
}

func requestDetailHasTokens(detail RequestDetail) bool {
	t := detail.Tokens
	return t.InputTokens != 0 || t.OutputTokens != 0 || t.ReasoningTokens != 0 ||
		t.CachedTokens != 0 || t.CacheTokens != 0 || t.CacheWriteTokens != 0 || t.TotalTokens != 0
}

func upstreamAttributionRecordModel(record UsageRecord) string {
	return strings.ToLower(strings.TrimSpace(firstNonEmpty(record.Alias, record.Model)))
}

func upstreamAttributionDetailModel(detail RequestDetail) string {
	return strings.ToLower(strings.TrimSpace(detail.Model))
}

func deepSeekModelFamily(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if !strings.HasPrefix(model, "deepseek-") {
		return ""
	}
	for _, suffix := range []string{"-flash", "-pro"} {
		if strings.HasSuffix(model, suffix) && len(model) > len(suffix) {
			return strings.TrimSuffix(model, suffix)
		}
	}
	return model
}

func upstreamAttributionRecordClient(record UsageRecord) string {
	if hash := hashAPIKey(record.APIKey); hash != "" {
		return "hash:" + strings.ToLower(hash)
	}
	return ""
}

func upstreamAttributionDetailClient(detail RequestDetail) string {
	if hash := strings.TrimSpace(detail.APIKeyHash); hash != "" {
		return "hash:" + strings.ToLower(hash)
	}
	label := canonicalClientAPIKey(detail.APIKey)
	if label == "" || strings.Contains(label, redactedMarker) {
		return ""
	}
	if hash := hashAPIKey(label); hash != "" {
		return "hash:" + strings.ToLower(hash)
	}
	return ""
}

func upstreamAttributionRecordLookupKeys(record UsageRecord) []string {
	return upstreamAttributionLookupKeys(upstreamAttributionRecordClient(record), record.Endpoint, upstreamAttributionRecordModel(record))
}

func upstreamAttributionRecordLearnKeys(record UsageRecord) []string {
	return upstreamAttributionLearnKeys(upstreamAttributionRecordClient(record), record.Endpoint, upstreamAttributionRecordModel(record))
}

func upstreamAttributionDetailLookupKeys(detail RequestDetail) []string {
	return upstreamAttributionLookupKeys(upstreamAttributionDetailClient(detail), detail.Endpoint, upstreamAttributionDetailModel(detail))
}

func upstreamAttributionDetailLearnKeys(detail RequestDetail) []string {
	return upstreamAttributionLearnKeys(upstreamAttributionDetailClient(detail), detail.Endpoint, upstreamAttributionDetailModel(detail))
}

func upstreamAttributionLookupKeys(client, endpoint, model string) []string {
	client = strings.TrimSpace(client)
	model = strings.ToLower(strings.TrimSpace(model))
	if client == "" || !strings.HasPrefix(model, "deepseek-") {
		return nil
	}
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	scope := "any"
	if endpoint != "" {
		scope = "endpoint:" + endpoint
	}
	return upstreamAttributionScopedKeys(client, scope, model)
}

func upstreamAttributionLearnKeys(client, endpoint, model string) []string {
	client = strings.TrimSpace(client)
	model = strings.ToLower(strings.TrimSpace(model))
	if client == "" || !strings.HasPrefix(model, "deepseek-") {
		return nil
	}
	keys := make([]string, 0, 4)
	if endpoint = strings.ToLower(strings.TrimSpace(endpoint)); endpoint != "" {
		keys = append(keys, upstreamAttributionScopedKeys(client, "endpoint:"+endpoint, model)...)
	}
	keys = append(keys, upstreamAttributionScopedKeys(client, "any", model)...)
	return uniqueStrings(keys)
}

func upstreamAttributionScopedKeys(client, scope, model string) []string {
	prefix := client + "\x00" + scope + "\x00"
	keys := []string{prefix + model}
	if family := deepSeekModelFamily(model); family != "" && family != model {
		keys = append(keys, prefix+family)
	}
	return keys
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func upstreamRouteIdentityFromRecord(record UsageRecord) upstreamRouteIdentity {
	return upstreamRouteIdentity{
		Provider:  strings.TrimSpace(record.Provider),
		Source:    safeUpstreamAttributionSource(record.Source, record.Provider),
		AuthID:    strings.TrimSpace(record.AuthID),
		AuthIndex: strings.TrimSpace(record.AuthIndex),
		AuthType:  strings.TrimSpace(record.AuthType),
		BaseURL:   strings.TrimSpace(record.BaseURL),
	}
}

func upstreamRouteIdentityFromDetail(detail RequestDetail) upstreamRouteIdentity {
	return upstreamRouteIdentity{
		Provider:  strings.TrimSpace(detail.Provider),
		Source:    safeUpstreamAttributionSource(detail.Source, detail.Provider),
		AuthID:    strings.TrimSpace(detail.AuthID),
		AuthIndex: strings.TrimSpace(detail.AuthIndex),
		AuthType:  strings.TrimSpace(detail.AuthType),
		BaseURL:   strings.TrimSpace(detail.BaseURL),
	}
}

func safeUpstreamAttributionSource(source, provider string) string {
	source = strings.TrimSpace(source)
	if source == "" || looksLikeSecretKey(source) || looksLikeCredentialID(source) {
		return strings.TrimSpace(provider)
	}
	return source
}

func sameUpstreamRoute(left, right upstreamRouteIdentity) bool {
	return strings.EqualFold(strings.TrimSpace(left.Provider), strings.TrimSpace(right.Provider)) &&
		strings.EqualFold(strings.TrimSpace(left.AuthID), strings.TrimSpace(right.AuthID)) &&
		strings.EqualFold(strings.TrimSpace(left.AuthIndex), strings.TrimSpace(right.AuthIndex))
}

func applyUpstreamRoute(record UsageRecord, route upstreamRouteIdentity) UsageRecord {
	if usageProviderFamily(record.Provider) == "claude" && usageProviderFamily(route.Provider) != "claude" {
		record.Detail = normalizeAnthropicUsageDetail(record.Detail, usageProviderFamily(route.Provider))
	}
	record.Provider = strings.TrimSpace(route.Provider)
	record.Source = safeUpstreamAttributionSource(route.Source, route.Provider)
	record.AuthID = strings.TrimSpace(route.AuthID)
	record.AuthIndex = strings.TrimSpace(route.AuthIndex)
	record.AuthType = strings.TrimSpace(route.AuthType)
	record.BaseURL = strings.TrimSpace(route.BaseURL)
	return record
}

func applyUpstreamRouteToDetail(detail RequestDetail, route upstreamRouteIdentity) RequestDetail {
	if usageProviderFamily(detail.Provider) == "claude" && usageProviderFamily(route.Provider) != "claude" {
		cacheTokens := normalizedCacheTokens(detail.Tokens)
		if cacheTokens > 0 {
			detail.Tokens.InputTokens += cacheTokens
			minimumTotal := detail.Tokens.InputTokens + detail.Tokens.OutputTokens
			if detail.Tokens.TotalTokens < minimumTotal {
				detail.Tokens.TotalTokens = minimumTotal
			}
		}
	}
	detail.Provider = strings.TrimSpace(route.Provider)
	detail.Source = safeUpstreamAttributionSource(route.Source, route.Provider)
	detail.AuthID = strings.TrimSpace(route.AuthID)
	detail.AuthIndex = strings.TrimSpace(route.AuthIndex)
	detail.AuthType = strings.TrimSpace(route.AuthType)
	detail.BaseURL = strings.TrimSpace(route.BaseURL)
	return detail
}

func recordUsageWithAttribution(record UsageRecord) {
	if upstreamAttributions == nil {
		stats.Record(record)
		return
	}
	learned := upstreamAttributions.Submit(record, stats.Record)
	if learned && stats != nil {
		stats.ReconcileUpstreamAttributions()
	}
}

func reconcileUpstreamAttributionSnapshot(snapshot StatisticsSnapshot, resolver *upstreamRouteResolver) (StatisticsSnapshot, int) {
	if resolver == nil {
		resolver = newUpstreamRouteResolver(maxHistoricalAttributionRoutes)
	}
	for _, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				detail.Model = normalizeDetailModelName(modelName, detail.Model)
				resolver.learnDetail(detail)
			}
		}
	}

	result := snapshot
	result.APIs = make(map[string]APISnapshot, len(snapshot.APIs))
	var migrations []upstreamSnapshotAttributionMigration
	corrected := 0
	for apiName, apiSnapshot := range snapshot.APIs {
		apiCopy := apiSnapshot
		apiCopy.Models = make(map[string]ModelSnapshot, len(apiSnapshot.Models))
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelCopy := modelSnapshot
			modelCopy.Details = append([]RequestDetail(nil), modelSnapshot.Details...)
			for i := range modelCopy.Details {
				detail := modelCopy.Details[i]
				detail.Model = normalizeDetailModelName(modelName, detail.Model)
				modelCopy.Details[i].Model = detail.Model
				if !isSuspiciousClaudeDeepSeekDetail(detail) {
					continue
				}
				route, ok := resolver.lookupDetail(detail)
				if !ok {
					continue
				}
				migrated := applyUpstreamRouteToDetail(detail, route)
				modelCopy.Details[i] = migrated
				migrations = append(migrations, upstreamSnapshotAttributionMigration{
					modelName: normalizeModelName(modelName), original: detail, corrected: migrated,
				})
				corrected++
			}
			apiCopy.Models[modelName] = modelCopy
		}
		result.APIs[apiName] = apiCopy
	}
	if len(migrations) > 0 {
		reconcileUpstreamAttributionCostSeries(&result, migrations)
	}
	return result, corrected
}

type upstreamSnapshotAttributionMigration struct {
	modelName string
	original  RequestDetail
	corrected RequestDetail
}

func reconcileUpstreamAttributionCostSeries(snapshot *StatisticsSnapshot, migrations []upstreamSnapshotAttributionMigration) {
	if snapshot == nil || len(migrations) == 0 {
		return
	}
	daySeries := timeSeriesTokenStatsByDayFromSnapshot(snapshot.CostTokensByDay)
	hourSeries := timeSeriesTokenStatsByHourFromSnapshot(snapshot.CostTokensByHour)
	for _, migration := range migrations {
		originalTotals := detailTotalsFromRequest(migration.original)
		correctedTotals := detailTotalsFromRequest(migration.corrected)
		modelName := detailModel(migration.modelName, migration.original)
		correctedModel := detailModel(migration.modelName, migration.corrected)
		day := migration.original.Timestamp.Format("2006-01-02")
		if decrementTimeSeriesTokenStats(daySeries[day], modelName, migration.original.Provider, originalTotals) {
			daySeries[day] = incrementTimeSeriesTokenStats(daySeries[day], correctedModel, migration.corrected.Provider, correctedTotals)
		}
		hour := migration.original.Timestamp.Hour()
		if decrementTimeSeriesTokenStats(hourSeries[hour], modelName, migration.original.Provider, originalTotals) {
			hourSeries[hour] = incrementTimeSeriesTokenStats(hourSeries[hour], correctedModel, migration.corrected.Provider, correctedTotals)
		}
	}
	snapshot.CostTokensByDay = timeSeriesTokenStatsByDaySnapshot(daySeries)
	snapshot.CostTokensByHour = timeSeriesTokenStatsByHourSnapshot(hourSeries)
	// Monetary totals depend on provider-scoped prices. Force them to be
	// recomputed from the corrected token series after prices are loaded.
	snapshot.CostByDay = nil
	snapshot.CostByHour = nil
}

func reconcilePersistedUpstreamAttributions(records []persistedDetail, resolver *upstreamRouteResolver) []persistedDetail {
	if resolver == nil {
		resolver = newUpstreamRouteResolver(maxHistoricalAttributionRoutes)
	}
	normalized := make([]RequestDetail, len(records))
	for i, record := range records {
		detail := record.Detail
		detail.Model = normalizeDetailModelName(record.Model, detail.Model)
		detail.Tokens.TotalTokens = detailTotalTokensForRequest(detail)
		detail.Source = cleanImportedDetailSource(detail)
		detail = normalizeStoredClientAPIIdentity(detail)
		normalized[i] = detail
		resolver.learnDetail(detail)
	}
	result := append([]persistedDetail(nil), records...)
	for i, detail := range normalized {
		if !isSuspiciousClaudeDeepSeekDetail(detail) {
			continue
		}
		route, ok := resolver.lookupDetail(detail)
		if !ok {
			continue
		}
		corrected := applyUpstreamRouteToDetail(result[i].Detail, route)
		corrected.Model = detail.Model
		result[i].Detail = corrected
	}
	return result
}

type recordedUpstreamAttributionMigration struct {
	apiName   string
	modelName string
	detail    RequestDetail
}

func (s *RequestStatistics) upstreamRouteResolverLocked() *upstreamRouteResolver {
	resolver := newUpstreamRouteResolver(maxHistoricalAttributionRoutes)
	if s == nil {
		return resolver
	}
	for _, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for _, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for _, detail := range modelSt.Details {
				resolver.learnDetail(detail)
			}
		}
	}
	return resolver
}

func (s *RequestStatistics) reconcileRecordedUpstreamAttributionsLocked(now time.Time) int {
	if s == nil {
		return 0
	}
	return s.reconcileRecordedUpstreamAttributionsWithResolverLocked(now, s.upstreamRouteResolverLocked())
}

func (s *RequestStatistics) reconcileRecordedUpstreamAttributionsWithResolverLocked(now time.Time, resolver *upstreamRouteResolver) int {
	if s == nil || resolver == nil {
		return 0
	}
	var migrations []recordedUpstreamAttributionMigration
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil || len(modelSt.Details) == 0 {
				continue
			}
			kept := make([]RequestDetail, 0, len(modelSt.Details))
			for _, detail := range modelSt.Details {
				if !isSuspiciousClaudeDeepSeekDetail(detail) {
					kept = append(kept, detail)
					continue
				}
				route, ok := resolver.lookupDetail(detail)
				if !ok {
					kept = append(kept, detail)
					continue
				}
				corrected := applyUpstreamRouteToDetail(detail, route)
				destination := usageGroupKeyFromDetail(apiName, corrected)
				s.decrementCounters(detail, apiSt, modelSt, modelName)
				migrations = append(migrations, recordedUpstreamAttributionMigration{
					apiName: destination, modelName: modelName, detail: corrected,
				})
			}
			modelSt.Details = kept
		}
	}

	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			delete(s.apis, apiName)
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil || (len(modelSt.Details) == 0 && modelSt.TotalRequests <= 0) {
				delete(apiSt.Models, modelName)
			}
		}
		if len(apiSt.Models) == 0 && apiSt.TotalRequests <= 0 {
			delete(s.apis, apiName)
		}
	}
	for _, migration := range migrations {
		s.recordDetailLocked(migration.apiName, migration.modelName, migration.detail, requestDedupKey{}, now, false)
	}
	if len(migrations) > 0 {
		s.pruneLocked(now, true)
		s.rebuildSeenLocked(now)
		s.invalidateSummaryLocked()
	}
	return len(migrations)
}

func (s *RequestStatistics) ReconcileUpstreamAttributions() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	corrected := s.reconcileRecordedUpstreamAttributionsLocked(time.Now())
	s.mu.Unlock()
	return corrected
}

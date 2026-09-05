package main

import (
	"strings"
	"time"
)

// v2.5.5 ~ v2.5.7 的「DeepSeek 上游归因兼容」会把成功的 claude:apikey DeepSeek
// 明细改写为同一客户端使用过的 openai-compatible-<name> 身份。该机制的前提不成立:
// CPA 原生 usage 的身份取自实际执行请求的 auth(NewUsageReporter 直接读取
// auth.ID / auth.EnsureIndex / executor.Identifier),claude:apikey + DeepSeek
// 意味着请求真的由 Claude 协议中转上游执行,而非误标。当两个上游提供同名模型时,
// 改写会把真实的 Claude 上游记录整体迁走(用户观察到「过一段时间全部变成
// opencode-go」)。改写机制已删除;本文件负责修复旧版本已经写坏的数据。
//
// 旧改写完整保留了原始明细的时间戳(纳秒)、延迟、TTFT、客户端与输出 token,
// 只替换身份字段并把缓存 token 折进输入。因此当原始 Claude 明细与其改写副本
// 同时在场(快照 vs JSONL 重放、或导出备份 vs 已损坏的现场),副本可以由这组
// 指纹唯一识别,丢弃副本、保留原始明细。

type attributionTwinKey struct {
	model     string
	timestamp time.Time
	latencyMs int64
	ttftMs    int64
	client    string
	output    int64
	reasoning int64
}

// attributionTwinClient 归一化客户端标识。导入的导出文件按跨实例合并语义只保留
// 脱敏展示值(hash 被清空),因此优先使用脱敏标签,原始标签先脱敏再参与比较,
// 保证「现场记录 vs 导入备份」两种表示能够对上;无标签时退回 hash。
func attributionTwinClient(detail RequestDetail) string {
	if label := canonicalClientAPIKey(detail.APIKey); label != "" {
		if !strings.Contains(label, redactedMarker) {
			label = maskAPIKey(label)
		}
		return "key:" + strings.ToLower(label)
	}
	if hash := strings.TrimSpace(detail.APIKeyHash); hash != "" {
		return "hash:" + strings.ToLower(hash)
	}
	return ""
}

func attributionTwinKeyFromDetail(detail RequestDetail) (attributionTwinKey, bool) {
	client := attributionTwinClient(detail)
	if client == "" || detail.Timestamp.IsZero() {
		return attributionTwinKey{}, false
	}
	return attributionTwinKey{
		model:     strings.ToLower(strings.TrimSpace(detail.Model)),
		timestamp: detail.Timestamp.UTC().Round(0),
		latencyMs: detail.LatencyMs,
		ttftMs:    detail.TTFTMs,
		client:    client,
		output:    detail.Tokens.OutputTokens,
		reasoning: detail.Tokens.ReasoningTokens,
	}, true
}

func requestDetailHasTokens(detail RequestDetail) bool {
	t := detail.Tokens
	return t.InputTokens != 0 || t.OutputTokens != 0 || t.ReasoningTokens != 0 ||
		t.CachedTokens != 0 || t.CacheTokens != 0 || t.CacheWriteTokens != 0 || t.TotalTokens != 0
}

// isOriginalClaudeDeepSeekDetail 匹配旧改写的目标形态:成功且原生归属
// claude:apikey 上游的 DeepSeek 明细。
func isOriginalClaudeDeepSeekDetail(detail RequestDetail) bool {
	if detail.Failed || !requestDetailHasTokens(detail) || usageProviderFamily(detail.Provider) != "claude" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(detail.AuthID)), "claude:apikey:") {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(detail.Model)), "deepseek-")
}

// isMigratedAttributionShapeDetail 匹配旧改写的产物形态:携带具名
// openai-compatible 身份的成功 DeepSeek 明细。
func isMigratedAttributionShapeDetail(detail RequestDetail) bool {
	if detail.Failed || !requestDetailHasTokens(detail) {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(detail.Provider))
	if !strings.HasPrefix(provider, "openai-compatible-") {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(detail.AuthID)), "openai-compatibility:") {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(detail.Model)), "deepseek-")
}

// attributionTwinTokensMatch 校验旧改写留下的 token 关系:副本的输入 token
// 等于原始输入,或等于原始输入加上被折入的缓存 token。缓存字段按归一化口径
// 比较,因为现场记录与导出/JSONL 对 CacheTokens 合计字段的填法不同。
func attributionTwinTokensMatch(original, migrated RequestDetail) bool {
	ot, mt := original.Tokens, migrated.Tokens
	if mt.OutputTokens != ot.OutputTokens || mt.ReasoningTokens != ot.ReasoningTokens {
		return false
	}
	if normalizedCacheReadTokens(mt) != normalizedCacheReadTokens(ot) ||
		normalizedCacheTokens(mt) != normalizedCacheTokens(ot) {
		return false
	}
	cachedTokens := nonNegativeInt64(ot.CachedTokens)
	inputTokens := nonNegativeInt64(ot.InputTokens)
	folds := []int64{
		0,
		normalizedCacheTokens(ot),
		cachedTokens,
	}
	for _, fold := range folds {
		if expected, ok := checkedProtocolAdd(inputTokens, fold); ok && mt.InputTokens == expected {
			return true
		}
	}
	if fold, ok := checkedProtocolAdd(cachedTokens, nonNegativeInt64(ot.CacheWriteTokens)); ok {
		if expected, ok := checkedProtocolAdd(inputTokens, fold); ok && mt.InputTokens == expected {
			return true
		}
	}
	return false
}

// repairMigratedAttributionDetailsLocked 丢弃与在场 Claude 原始明细构成孪生对的
// 改写副本,返回被移除副本的去重键。孪生对只会跨来源出现(旧快照 vs 原始
// JSONL、损坏现场 vs 导出备份),同一来源内每个请求只有一条记录。
func (s *RequestStatistics) repairMigratedAttributionDetailsLocked(now time.Time) []requestDedupKey {
	if s == nil {
		return nil
	}
	originals := make(map[attributionTwinKey][]RequestDetail)
	for _, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for _, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for _, detail := range modelSt.Details {
				if !isOriginalClaudeDeepSeekDetail(detail) {
					continue
				}
				if key, ok := attributionTwinKeyFromDetail(detail); ok {
					originals[key] = append(originals[key], detail)
				}
			}
		}
	}
	if len(originals) == 0 {
		return nil
	}

	var removed []requestDedupKey
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil || len(modelSt.Details) == 0 {
				continue
			}
			kept := modelSt.Details[:0]
			for _, detail := range modelSt.Details {
				if !s.isRepairableMigratedTwinLocked(originals, detail) {
					kept = append(kept, detail)
					continue
				}
				s.decrementCounters(detail, apiSt, modelSt, modelName)
				removed = append(removed, dedupKey(apiName, modelName, detail))
			}
			modelSt.Details = kept
		}
	}
	if len(removed) == 0 {
		return nil
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
	s.rebuildSeenLocked(now)
	s.invalidateSummaryLocked()
	return removed
}

func (s *RequestStatistics) isRepairableMigratedTwinLocked(originals map[attributionTwinKey][]RequestDetail, detail RequestDetail) bool {
	if !isMigratedAttributionShapeDetail(detail) {
		return false
	}
	key, ok := attributionTwinKeyFromDetail(detail)
	if !ok {
		return false
	}
	for _, original := range originals[key] {
		if attributionTwinTokensMatch(original, detail) {
			return true
		}
	}
	return false
}

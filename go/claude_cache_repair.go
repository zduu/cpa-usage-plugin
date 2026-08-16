package main

import (
	"time"
)

// CPA 的 parseClaudeUsageNode 在 cache_read 为 0 时把 cache_creation 回填进
// CachedTokens。修复前的插件版本把该回填值当作缓存命中入账,导致 Claude 家族
// 记录的缓存创建被双计:命中 +X、总量 +X。入口污染已在 usageDetailCacheTokenParts
// 剔除;本文件负责修复已入库的历史明细。被污染明细有精确签名(命中 == 创建 == X,
// 缓存合计 == 2X,总量 == input+output+2X,全部由回填构造保证),按不变量还原:
// 命中归零、缓存合计与总量各减去一份创建量。
//
// 与「真实命中恰好等于创建量」合法记录的区分依据 Tokens.CacheReadTokens:修复后
// 的入口把权威命中值一并持久化,带该字段的明细一律免除修复;旧版本写入的明细
// 没有该字段,按签名修复。归一化在重放与导入的入库口执行,保证同一请求在旧快照
// 与 JSONL 分片中的两份形态归一到相同的去重键,不会双计。

func isPollutedClaudeCacheFallbackDetail(detail RequestDetail) bool {
	if detail.Failed || usageProviderFamily(detail.Provider) != "claude" {
		return false
	}
	t := detail.Tokens
	// 修复后的入口写入的明细带权威命中字段,不参与历史修复。
	if nonNegativeInt64(t.CacheReadTokens) > 0 {
		return false
	}
	write := nonNegativeInt64(t.CacheWriteTokens)
	if write == 0 || nonNegativeInt64(t.CachedTokens) != write {
		return false
	}
	if nonNegativeInt64(t.CacheTokens) != write*2 {
		return false
	}
	expected := nonNegativeInt64(t.InputTokens) + nonNegativeInt64(t.OutputTokens) + write*2
	return nonNegativeInt64(t.TotalTokens) == expected
}

func repairClaudeCacheFallbackTokens(detail RequestDetail) RequestDetail {
	write := nonNegativeInt64(detail.Tokens.CacheWriteTokens)
	detail.Tokens.CachedTokens = 0
	detail.Tokens.CacheTokens = write
	detail.Tokens.TotalTokens = nonNegativeInt64(detail.Tokens.InputTokens) +
		nonNegativeInt64(detail.Tokens.OutputTokens) + write
	return detail
}

// normalizeClaudeCacheFallbackDetail 是重放与导入入库口的归一化:污染明细在
// 计算去重键与持久化之前先还原,使其与已修复的内存态/快照态使用同一个键。
func normalizeClaudeCacheFallbackDetail(detail RequestDetail) RequestDetail {
	if isPollutedClaudeCacheFallbackDetail(detail) {
		return repairClaudeCacheFallbackTokens(detail)
	}
	return detail
}

type claudeCacheFallbackRepair struct {
	apiName   string
	modelName string
	detail    RequestDetail
}

// repairClaudeCacheFallbackDetailsLocked 还原被双计的历史明细:摘除污染明细并
// 回退其计入的全部计数,再以修复后的 token 重新入账。返回修复条数。
func (s *RequestStatistics) repairClaudeCacheFallbackDetailsLocked(now time.Time) int {
	if s == nil {
		return 0
	}
	var repairs []claudeCacheFallbackRepair
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
				if !isPollutedClaudeCacheFallbackDetail(detail) {
					kept = append(kept, detail)
					continue
				}
				s.decrementCounters(detail, apiSt, modelSt, modelName)
				repairs = append(repairs, claudeCacheFallbackRepair{
					apiName:   apiName,
					modelName: modelName,
					detail:    repairClaudeCacheFallbackTokens(detail),
				})
			}
			modelSt.Details = kept
		}
	}
	if len(repairs) == 0 {
		return 0
	}
	for _, repair := range repairs {
		s.recordDetailLocked(repair.apiName, repair.modelName, repair.detail, requestDedupKey{}, now, false)
	}
	s.rebuildSeenLocked(now)
	s.invalidateSummaryLocked()
	return len(repairs)
}

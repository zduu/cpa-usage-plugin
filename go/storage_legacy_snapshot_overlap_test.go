package main

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

// legacySnapshotDir 生成一个 v2.6.4 形态的存储目录:快照只保存被明细上限截断后的可见
// 明细,超出上限的请求只体现在汇总计数里(模型快照没有 accounting),而当天 JSONL 仍然
// 包含全部请求。这正是从旧版升级时重放会重复入账的现场。
func legacySnapshotDir(t *testing.T, maxDetails int, requests int) string {
	t.Helper()
	dir := t.TempDir()
	seed := NewRequestStatistics()
	seed.Configure(runtimeConfig{StorageEnabled: true, StoragePath: dir, MaxDetailsPerModel: maxDetails, RetentionDays: 30})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < requests; i++ {
		seed.Record(UsageRecord{Provider: "test", Model: "legacy-model", RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail: UsageDetail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}})
	}
	seed.Close()

	raw, err := os.ReadFile(storageSnapshotPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var persisted persistedStorageSnapshot
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	stripped := 0
	for apiName, apiSnapshot := range persisted.Usage.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			stripped += len(modelSnapshot.Accounting)
			modelSnapshot.Accounting = nil
			apiSnapshot.Models[modelName] = modelSnapshot
		}
		persisted.Usage.APIs[apiName] = apiSnapshot
	}
	if stripped == 0 {
		t.Fatal("fixture did not exercise the legacy snapshot shape")
	}
	legacy, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storageSnapshotPath(dir), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func openStorageDir(t *testing.T, dir string, maxDetails int) *RequestStatistics {
	t.Helper()
	s := NewRequestStatistics()
	s.Configure(runtimeConfig{StorageEnabled: true, StoragePath: dir, MaxDetailsPerModel: maxDetails, RetentionDays: 30})
	if err := s.StorageStatus().LastError; err != "" {
		t.Fatal(err)
	}
	return s
}

// 旧版快照的残差与当天 JSONL 重叠:重放时必须只消耗残差,不能把已经计入累计值的请求
// 再加一次。修复前这里会得到 5(3 条 + 2 条被截断的重复记录)。
func TestStorageLegacySnapshotOverlapDoesNotDoubleCount(t *testing.T) {
	for _, maxDetails := range []int{1, 2} {
		t.Run("max_details="+strconv.Itoa(maxDetails), func(t *testing.T) {
			dir := legacySnapshotDir(t, maxDetails, 3)

			s := openStorageDir(t, dir, maxDetails)
			snapshot := s.Snapshot()
			if snapshot.TotalRequests != 3 || snapshot.InputTokens != 30 || snapshot.TotalTokens != 45 {
				t.Fatalf("upgrade re-counted overlapping requests: requests=%d input=%d tokens=%d, want 3/30/45",
					snapshot.TotalRequests, snapshot.InputTokens, snapshot.TotalTokens)
			}
			s.Close()

			// 第二次普通重启必须保持同样的数值,而不是逐次累加。
			again := openStorageDir(t, dir, maxDetails)
			defer again.Close()
			if snapshot := again.Snapshot(); snapshot.TotalRequests != 3 || snapshot.InputTokens != 30 {
				t.Fatalf("restart changed totals: requests=%d input=%d, want 3/30", snapshot.TotalRequests, snapshot.InputTokens)
			}
		})
	}
}

// 残差只能吸收快照已经计入的旧记录,升级之后新产生的请求仍然必须正常入账。
func TestStorageLegacySnapshotResidualKeepsNewRequests(t *testing.T) {
	dir := legacySnapshotDir(t, 2, 3)

	s := openStorageDir(t, dir, 2)
	for i := 0; i < 2; i++ {
		s.Record(UsageRecord{Provider: "test", Model: "legacy-model", RequestedAt: time.Now(),
			Detail: UsageDetail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}})
	}
	if snapshot := s.Snapshot(); snapshot.TotalRequests != 5 || snapshot.InputTokens != 50 {
		t.Fatalf("new requests were not counted: requests=%d input=%d, want 5/50", snapshot.TotalRequests, snapshot.InputTokens)
	}
	s.Close()

	again := openStorageDir(t, dir, 2)
	defer again.Close()
	if snapshot := again.Snapshot(); snapshot.TotalRequests != 5 || snapshot.InputTokens != 50 || snapshot.TotalTokens != 75 {
		t.Fatalf("restart changed totals: requests=%d input=%d tokens=%d, want 5/50/75",
			snapshot.TotalRequests, snapshot.InputTokens, snapshot.TotalTokens)
	}
}

// 升级后导入或记录时间戳更早的历史数据,不能让残差吸收失效:否则之后每次重启都会把
// 被截断的旧记录重新计入。曾用「残差必然早于仍可寻址记录」的排序边界收紧吸收,这个
// 场景会让该前提不成立,重复入账随之回来,因此不再使用排序边界。
func TestStorageLegacySnapshotResidualSurvivesOlderHistory(t *testing.T) {
	dir := legacySnapshotDir(t, 2, 3)

	s := openStorageDir(t, dir, 2)
	if got := s.Snapshot().TotalRequests; got != 3 {
		t.Fatalf("upgrade changed totals: requests=%d, want 3", got)
	}
	// 比所有既有记录都早的一条历史数据,模拟升级后导入旧备份。
	s.Record(UsageRecord{Provider: "test", Model: "legacy-model", RequestedAt: time.Now().Add(-72 * time.Hour),
		Detail: UsageDetail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}})
	if got := s.Snapshot().TotalRequests; got != 4 {
		t.Fatalf("older history was not recorded: requests=%d, want 4", got)
	}
	s.Close()

	again := openStorageDir(t, dir, 2)
	defer again.Close()
	if snapshot := again.Snapshot(); snapshot.TotalRequests != 4 || snapshot.InputTokens != 40 {
		t.Fatalf("older history broke residual absorption: requests=%d input=%d, want 4/40",
			snapshot.TotalRequests, snapshot.InputTokens)
	}
}

// 对照:候选自身写出的快照带 accounting,逐请求条目齐全,不需要残差也不受影响。
func TestStorageCandidateSnapshotKeepsEveryRequestAddressable(t *testing.T) {
	dir := t.TempDir()
	seed := NewRequestStatistics()
	seed.Configure(runtimeConfig{StorageEnabled: true, StoragePath: dir, MaxDetailsPerModel: 2, RetentionDays: 30})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		seed.Record(UsageRecord{Provider: "test", Model: "legacy-model", RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail: UsageDetail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}})
	}
	seed.Close()

	s := openStorageDir(t, dir, 2)
	defer s.Close()
	snapshot := s.Snapshot()
	if snapshot.TotalRequests != 3 || snapshot.InputTokens != 30 {
		t.Fatalf("candidate snapshot changed totals: requests=%d input=%d, want 3/30", snapshot.TotalRequests, snapshot.InputTokens)
	}
	s.mu.RLock()
	var unaddressable int64
	addressable := 0
	for _, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for _, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			count := modelSt.accountingCount()
			addressable += count
			unaddressable += modelSt.TotalRequests - int64(count)
		}
	}
	s.mu.RUnlock()
	if unaddressable != 0 || addressable != 3 {
		t.Fatalf("candidate snapshot left %d unaddressable requests (addressable=%d), want 0/3", unaddressable, addressable)
	}
}

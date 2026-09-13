package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// legacySnapshotFixture 生成一个 v2.6.4 形态的存储目录:快照只保存被明细上限截断后的可见
// 明细,超出上限的请求只体现在汇总计数里(模型快照没有 accounting),而当天 JSONL 仍然
// 包含全部请求。这正是从旧版升级时重放会重复入账的现场。base 决定请求时间;generatedAt
// 非零时改写快照生成时间,用于让多个日分片落在重放窗口内。
func legacySnapshotFixture(t *testing.T, maxDetails int, requests int, base time.Time, generatedAt time.Time) string {
	t.Helper()
	dir := t.TempDir()
	seed := NewRequestStatistics()
	seed.Configure(runtimeConfig{StorageEnabled: true, StoragePath: dir, MaxDetailsPerModel: maxDetails, RetentionDays: 30})
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
	if !generatedAt.IsZero() {
		persisted.GeneratedAt = generatedAt.UTC().Format(time.RFC3339)
	}
	legacy, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storageSnapshotPath(dir), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if stripSnapshotAccounting(t, dir) == 0 {
		t.Fatal("fixture did not exercise the legacy snapshot shape")
	}
	return dir
}

// stripSnapshotAccounting 去掉模型快照里的逐请求账本,把候选写出的快照还原成 v2.6.4
// 及更早版本的文件形态:只有被明细上限截断后的可见明细。
func stripSnapshotAccounting(t *testing.T, dir string) int {
	t.Helper()
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
	legacy, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storageSnapshotPath(dir), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	return stripped
}

func legacySnapshotDir(t *testing.T, maxDetails int, requests int) string {
	t.Helper()
	return legacySnapshotFixture(t, maxDetails, requests, time.Now().Add(-time.Hour), time.Time{})
}

// appendStorageRecord 把一条记录追加到指定日期的分片,用于构造跨日重放现场。接口分组
// 与模型沿用已有分片里的取值,保证重放时能和快照里的分组对上。
func appendStorageRecord(t *testing.T, dir string, at time.Time, requestedAt time.Time) {
	t.Helper()
	shard := filepath.Join(dir, storageFileName(storageDate(at)))
	existing, err := os.ReadFile(shard)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var template persistedDetail
	if len(existing) > 0 {
		firstLine, _, _ := bytes.Cut(existing, []byte("\n"))
		if err := json.Unmarshal(firstLine, &template); err != nil {
			t.Fatal(err)
		}
	}
	record := persistedDetail{
		API:   firstNonEmpty(template.API, "test"),
		Model: firstNonEmpty(template.Model, "legacy-model"),
		Detail: RequestDetail{
			Model: firstNonEmpty(template.Detail.Model, "legacy-model"), Provider: firstNonEmpty(template.Detail.Provider, "test"),
			Timestamp: requestedAt,
			Tokens:    TokenStats{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shard, append(existing, append(line, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 重放窗口跨多个日分片时,吸收索引按文件重置,但残差额度仍保证被截断的旧记录只被吸收
// 一次,而快照之后写入的新记录照常入账。
func TestStorageLegacySnapshotResidualSpansShards(t *testing.T) {
	now := time.Now()
	// 固定在「昨天 00:00 UTC」这条日界上,保证快照日、旧分片日期与新记录日期三者
	// 的关系与运行时刻无关。
	base := now.UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	snapshotAt := base.Add(23 * time.Hour)
	dir := legacySnapshotFixture(t, 2, 3, base, snapshotAt)

	// 旧记录本来写在"今天"的分片里,改成"昨天";再为"今天"写入快照之后的新记录。
	if err := os.Rename(filepath.Join(dir, storageFileName(storageDate(now))),
		filepath.Join(dir, storageFileName(storageDate(base)))); err != nil {
		t.Fatal(err)
	}
	appendStorageRecord(t, dir, now, now)

	s := openStorageDir(t, dir, 2)
	defer s.Close()
	if snapshot := s.Snapshot(); snapshot.TotalRequests != 4 || snapshot.InputTokens != 40 {
		t.Fatalf("cross-shard replay miscounted: requests=%d input=%d, want 4/40",
			snapshot.TotalRequests, snapshot.InputTokens)
	}
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

// 随机化对照:旧版快照记了多少条请求,升级恢复后累计值就必须还是多少,重复重启也不变。
// 明细上限、保留窗口、模型数量、请求条数和时间戳分布逐轮变化,用来逼出「只在某种组合下
// 才成立」的假设——上一轮就是被这种组合推翻过一次。
func TestStorageLegacySnapshotUpgradePreservesTotals(t *testing.T) {
	for seed := 0; seed < 32; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(seed)))
			dir := t.TempDir()
			maxDetails := 1 + rng.Intn(4)
			models := 1 + rng.Intn(3)
			retentionDays := []int{1, 7, 30}[rng.Intn(3)]
			// 每条模型至少制造一条被截断的请求,再追加若干条随机冗余。
			records := maxDetails*models + 1 + rng.Intn(6)

			seedStats := NewRequestStatistics()
			seedStats.Configure(runtimeConfig{StorageEnabled: true, StoragePath: dir,
				MaxDetailsPerModel: maxDetails, RetentionDays: retentionDays})
			// 全部请求都落在最近 13 小时内,即使保留窗口设为 1 天也不会踩到保留边界,
			// 这样累计值的对照只反映重放语义,不掺入裁剪时点差异。
			anchor := time.Now().Add(-time.Duration(rng.Intn(30)) * time.Minute)
			for i := 0; i < records; i++ {
				seedStats.Record(UsageRecord{
					Provider: "test", Model: fmt.Sprintf("model-%d", i%models),
					RequestedAt: anchor.Add(-time.Duration(rng.Intn(12*60)) * time.Minute),
					Detail:      UsageDetail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				})
			}
			want := seedStats.Snapshot()
			if want.TotalRequests != int64(records) {
				t.Fatalf("fixture lost records before the upgrade: got %d, want %d", want.TotalRequests, records)
			}
			seedStats.Close()
			stripSnapshotAccounting(t, dir)

			upgraded := openStorageDir(t, dir, maxDetails)
			got := upgraded.Snapshot()
			if got.TotalRequests != want.TotalRequests || got.InputTokens != want.InputTokens || got.TotalTokens != want.TotalTokens {
				t.Fatalf("upgrade changed totals: requests=%d/%d input=%d/%d tokens=%d/%d",
					got.TotalRequests, want.TotalRequests, got.InputTokens, want.InputTokens, got.TotalTokens, want.TotalTokens)
			}
			upgraded.Close()

			restarted := openStorageDir(t, dir, maxDetails)
			defer restarted.Close()
			if again := restarted.Snapshot(); again.TotalRequests != want.TotalRequests || again.InputTokens != want.InputTokens {
				t.Fatalf("restart changed totals: requests=%d/%d input=%d/%d",
					again.TotalRequests, want.TotalRequests, again.InputTokens, want.InputTokens)
			}
		})
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

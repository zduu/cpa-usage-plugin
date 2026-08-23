package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func floatPtrForTest(value float64) *float64 { return &value }

func TestModelPriceTimeRulesKeepLegacyJSONCompatible(t *testing.T) {
	var legacy ModelPrice
	if err := json.Unmarshal([]byte(`{"prompt":1,"completion":2,"cache":0.1}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.CacheWrite != 0 || len(legacy.TimeRules) != 0 {
		t.Fatalf("legacy price = %#v", legacy)
	}
	zero := floatPtrForTest(0)
	price := ModelPrice{Prompt: 1, Completion: 2, Cache: 3, CacheWrite: 4, TimeRules: []ModelPriceRule{{ID: "peak", Name: "peak", Start: "22:00", End: "06:00", Prompt: zero}}}
	raw, err := json.Marshal(price)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip ModelPrice
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.TimeRules[0].Prompt == nil || *roundTrip.TimeRules[0].Prompt != 0 {
		t.Fatalf("zero override lost: %s", raw)
	}
}

func TestPricingRulesValidateBoundariesAndOverlap(t *testing.T) {
	valid := []ModelPriceRule{{ID: "a", Name: "a", Start: "00:00", End: "08:30", Prompt: floatPtrForTest(1)}, {ID: "b", Name: "b", Start: "08:30", End: "12:00", Completion: floatPtrForTest(2)}}
	if err := validateModelPriceRules(valid); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"8:30", "24:00", "12:60", "noon"} {
		if _, err := parsePricingMinute(value); err == nil {
			t.Fatalf("%q accepted", value)
		}
	}
	overlap := append([]ModelPriceRule(nil), valid...)
	overlap[1].Start = "08:00"
	if err := validateModelPriceRules(overlap); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap error = %v", err)
	}
	cross := []ModelPriceRule{{ID: "night", Name: "night", Start: "22:00", End: "06:00", Prompt: floatPtrForTest(1)}}
	if err := validateModelPriceRules(cross); err != nil {
		t.Fatal(err)
	}
	if !ruleContainsMinute(cross[0], 23*60) || !ruleContainsMinute(cross[0], 1) || ruleContainsMinute(cross[0], 12*60) {
		t.Fatal("cross-day matching is wrong")
	}
}

func TestEffectivePriceUsesTimezoneAndZeroOverride(t *testing.T) {
	zero := floatPtrForTest(0)
	base := ModelPrice{Prompt: 1, Completion: 2, Cache: 3, CacheWrite: 4, TimeRules: []ModelPriceRule{{Name: "peak", Start: "08:00", End: "09:00", Prompt: zero, Completion: floatPtrForTest(8)}}}
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	at := time.Date(2026, 8, 22, 0, 30, 0, 0, time.UTC)
	peak := effectivePrice(base, at, shanghai)
	if peak.Prompt != 0 || peak.Completion != 8 || peak.Cache != 3 {
		t.Fatalf("peak price = %#v", peak)
	}
	outside := effectivePrice(base, time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC), shanghai)
	if outside.Prompt != 1 || outside.Completion != 2 {
		t.Fatalf("outside price = %#v", outside)
	}
	if math.IsNaN(peak.Prompt) {
		t.Fatal("unexpected NaN")
	}
}

func TestTimePricingKeepsTrimmedCostsWhenRepricing(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 1,
		RetentionDays:      0,
		DedupWindowMinutes: 0,
		PriceStoragePath:   filepath.Join(t.TempDir(), "prices.json"),
	})
	if _, err := stats.UpsertModelPrice("deepseek-chat", ModelPrice{Prompt: 1}); err != nil {
		t.Fatalf("save base price: %v", err)
	}

	// 00:30 UTC is 08:30 in Shanghai and therefore in the later peak rule.
	base := time.Date(2026, 8, 22, 0, 30, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-chat",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{InputTokens: 100},
		})
	}

	price := ModelPrice{
		Prompt: 1,
		TimeRules: []ModelPriceRule{{
			Name: "morning-peak", Start: "08:00", End: "09:00", Prompt: floatPtrForTest(2),
		}},
	}
	if _, err := stats.UpsertModelPrice("deepseek-chat", price); err != nil {
		t.Fatalf("save time price: %v", err)
	}

	summary := stats.SummaryWithoutDetails()
	// Two old records have been trimmed. They retain their base-price cost;
	// the retained record receives the peak delta, so no historical cost is lost.
	assertFloatNear(t, "total cost after time reprice", summary.Usage.TotalCost, 0.0004)
	assertFloatNear(t, "daily cost after time reprice", summary.Usage.CostByDay["2026-08-22"], 0.0004)
	api := summary.Usage.APIs["deepseek"]
	assertFloatNear(t, "api cost after time reprice", api.EstimatedCost, 0.0004)
	assertFloatNear(t, "model cost after time reprice", api.Models["deepseek-chat"].EstimatedCost, 0.0004)
}

func TestExchangeRateParsingAndStateFallback(t *testing.T) {
	rate, err := parseExchangeRate([]byte(`{"rates":{"CNY":7.1234}}`))
	if err != nil || rate != 7.1234 {
		t.Fatalf("parse rate = %v, %v", rate, err)
	}
	if _, err := parseExchangeRate([]byte(`{"rates":{"CNY":2}}`)); err == nil {
		t.Fatal("out-of-range rate accepted")
	}

	stats := NewRequestStatistics()
	stats.exchangeRateEnabled = true
	stats.exchangeRateFallback = 7.2
	state := stats.currencyStateLocked(time.Now())
	if state.Status != "fallback" || state.USDCNYRate != 7.2 {
		t.Fatalf("fallback state = %#v", state)
	}
	stats.exchangeRate = 7.1
	stats.exchangeRateFetchedAt = time.Now().Add(-13 * time.Hour)
	stats.exchangeRateRefresh = 6 * time.Hour
	stats.exchangeRateLastError = "timeout"
	state = stats.currencyStateLocked(time.Now())
	if state.Status != "stale" || state.USDCNYRate != 7.1 {
		t.Fatalf("stale state = %#v", state)
	}
}

// 零价时段必须以 "cost_usd":0 出现在事件接口和导出里。若字段被 omitempty 抹掉,
// 看板会当作「后端没给成本」而按基础价格重算,把免费时段显示成收费。
func TestZeroCostEventsKeepExplicitCostField(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})
	at := recentRecordTime(stats.PricingLocation())
	start, end := hourWindowAround(t, at, stats.PricingLocation())
	price := ModelPrice{
		Prompt:     6,
		Completion: 6,
		TimeRules: []ModelPriceRule{{
			Name: "free-window", Start: start, End: end,
			Prompt: floatPtrForTest(0), Completion: floatPtrForTest(0),
		}},
	}
	if _, err := stats.UpsertModelPrice("deepseek-chat", price); err != nil {
		t.Fatalf("save price: %v", err)
	}
	// 该时刻落在上面按其本地整点推导出的零价时段内。
	stats.Record(UsageRecord{
		Provider:    "deepseek",
		Model:       "deepseek-chat",
		RequestedAt: at,
		Detail:      UsageDetail{InputTokens: 1000, OutputTokens: 1000},
	})

	events := stats.QueryEvents(EventsQuery{Limit: 10})
	if len(events.Events) != 1 {
		t.Fatalf("events = %d", len(events.Events))
	}
	event := events.Events[0]
	if event.CostUSD == nil {
		t.Fatal("cost_usd must be attached even when the cost is zero")
	}
	if *event.CostUSD != 0 {
		t.Fatalf("cost_usd = %v, want 0 inside the free window", *event.CostUSD)
	}

	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cost_usd":0`) {
		t.Fatalf("JSON/JSONL export dropped the zero cost: %s", raw)
	}

	record := dashboardEventCSVRecord(event)
	header := dashboardEventsCSVHeader()
	if len(record) != len(header) {
		t.Fatalf("CSV record has %d columns, header has %d", len(record), len(header))
	}
	if header[len(header)-1] != "cost_usd" {
		t.Fatalf("cost_usd must stay in the last column, header = %v", header)
	}
	if record[len(record)-1] != "0" {
		t.Fatalf("CSV cost column = %q, want %q", record[len(record)-1], "0")
	}

	// 未附加成本的存储记录不写出 cost_usd,以便与「确为零价」区分开。
	stored, err := json.Marshal(RequestDetail{Model: "deepseek-chat"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "cost_usd") {
		t.Fatalf("stored details must omit cost_usd: %s", stored)
	}
}

// retention 淘汰必须同步扣减 API / API 模型成本。存在时段价格时
// applySummaryEstimatedCostsLocked 不再从聚合 token 重算这两处,漏减会让过期请求
// 永久留在 API 成本里,与总成本和模型统计对不上。
func TestRetentionEvictionDecrementsAPIEstimatedCost(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100,
		RetentionDays:      1,
		PriceStoragePath:   filepath.Join(t.TempDir(), "prices.json"),
	})

	// Record() 按真实系统时间执行 retention 淘汰,所以时间点必须相对 time.Now()
	// 取,写死绝对时刻的话这条测试会在某个日期之后每天稳定失败。时段窗口同样按
	// 保留记录的本地时刻推导,才能不依赖跑测试的钟点。
	kept := time.Now().Add(-2 * time.Hour)
	evicted := time.Now().Add(-72 * time.Hour)
	start, end := hourWindowAround(t, kept, stats.PricingLocation())
	price := ModelPrice{
		Prompt:    6,
		TimeRules: []ModelPriceRule{{Name: "peak", Start: start, End: end, Prompt: floatPtrForTest(12)}},
	}
	if _, err := stats.UpsertModelPrice("deepseek-chat", price); err != nil {
		t.Fatalf("save price: %v", err)
	}

	for _, at := range []time.Time{evicted, kept} {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-chat",
			RequestedAt: at,
			Detail:      UsageDetail{InputTokens: 1000},
		})
	}

	summary := stats.SummaryWithoutDetails()
	api, ok := summary.Usage.APIs["deepseek"]
	if !ok {
		t.Fatalf("missing api stats: %#v", summary.Usage.APIs)
	}
	model, ok := findModelStat(summary.ModelStats, "deepseek-chat")
	if !ok {
		t.Fatalf("missing model stats")
	}
	if api.TotalRequests != 1 {
		t.Fatalf("api requests = %d, want only the retained one", api.TotalRequests)
	}
	// 只剩保留窗口内的那条请求,按 12/M 的峰值价计 1000 token。
	assertFloatNear(t, "api cost after retention eviction", api.EstimatedCost, 0.012)
	assertFloatNear(t, "api model cost after retention eviction", api.Models["deepseek-chat"].EstimatedCost, 0.012)
	assertFloatNear(t, "model cost after retention eviction", model.EstimatedCost, 0.012)
	assertFloatNear(t, "total cost after retention eviction", summary.Usage.TotalCost, 0.012)
}

// hourWindowAround 返回覆盖 at 所在本地整点的 [HH:00, HH+1:00) 时段。跨零点时
// end 会回绕成 "00:00",这仍是一条合法的跨日规则。
func hourWindowAround(t *testing.T, at time.Time, location *time.Location) (string, string) {
	t.Helper()
	if location == nil {
		location = time.UTC
	}
	hour := at.In(location).Hour()
	return fmt.Sprintf("%02d:00", hour), fmt.Sprintf("%02d:00", (hour+1)%24)
}

// recentRecordTime 返回 2 小时前、对齐到该本地小时 :30 的时刻。写死绝对日期的用例
// 会在超过 retention(默认 30 天)之后每天稳定失败,所以统计类用例的时间点一律相对
// time.Now() 取;对齐到 :30 是为了让 ±几分钟的记录仍落在同一个整点时段内。
func recentRecordTime(location *time.Location) time.Time {
	if location == nil {
		location = time.UTC
	}
	at := time.Now().Add(-2 * time.Hour).In(location)
	return time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), 30, 0, 0, location)
}

// 重复保存同一份价格不能让成本膨胀:时段差额只能叠加在本轮真正重建过的序列上。
func TestRepricingIsIdempotent(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})
	base := recentRecordTime(stats.PricingLocation())
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-chat",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{InputTokens: 1000},
		})
	}
	start, end := hourWindowAround(t, base, stats.PricingLocation())
	// costByDay 按 detail.Timestamp 自身时区的日期分组,不能转成 UTC 再取日期。
	dayKey := base.Format("2006-01-02")
	price := ModelPrice{
		Prompt:    6,
		TimeRules: []ModelPriceRule{{Name: "peak", Start: start, End: end, Prompt: floatPtrForTest(12)}},
	}
	var first float64
	for round := 0; round < 4; round++ {
		if _, err := stats.UpsertModelPrice("deepseek-chat", price); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		summary := stats.SummaryWithoutDetails()
		if round == 0 {
			first = summary.Usage.TotalCost
			assertFloatNear(t, "total cost", first, 0.036)
			continue
		}
		assertFloatNear(t, fmt.Sprintf("total cost after reprice round %d", round), summary.Usage.TotalCost, first)
		assertFloatNear(t, fmt.Sprintf("daily cost after reprice round %d", round), summary.Usage.CostByDay[dayKey], first)
	}
}

// 超过有效期就是 stale,哪怕没有记录到拉取错误(休眠、定时器被推迟、worker 没被
// 及时调度都不会留下 error,但汇率确实已经过期)。
func TestExchangeRateExpiresWithoutRecordedError(t *testing.T) {
	stats := NewRequestStatistics()
	stats.exchangeRateEnabled = true
	stats.exchangeRate = 7.1
	stats.exchangeRateRefresh = 6 * time.Hour
	stats.exchangeRateLastError = ""

	now := time.Now()
	stats.exchangeRateFetchedAt = now.Add(-1 * time.Hour)
	if state := stats.currencyStateLocked(now); state.Status != "fresh" {
		t.Fatalf("recent rate status = %q, want fresh", state.Status)
	}
	stats.exchangeRateFetchedAt = now.Add(-13 * time.Hour)
	if state := stats.currencyStateLocked(now); state.Status != "stale" {
		t.Fatalf("expired rate status = %q, want stale", state.Status)
	}
}

// 非法计费时区保留旧时区,但必须把原因带到运行状态和健康告警里。
func TestInvalidPricingTimezoneSurfacesInStatusAndHealth(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{PricingTimezone: "Not/AZone"})

	status := stats.RuntimeStatus()
	if status.PricingTimezone != defaultPricingTimezone {
		t.Fatalf("pricing timezone = %q, want the previous %q", status.PricingTimezone, defaultPricingTimezone)
	}
	if status.PricingTimezoneError == "" {
		t.Fatal("invalid pricing timezone must be reported in runtime status")
	}
	alerts := healthAlerts(StorageStatus{}, status)
	if !hasHealthAlertCode(alerts, "pricing_timezone_invalid") {
		t.Fatalf("missing pricing_timezone_invalid alert: %#v", alerts)
	}
}

// 图纸 5.2:stale、fallback 或持续拉取失败都要告警;汇率未启用时不告警。
func TestExchangeRateHealthAlertsFollowTheSpec(t *testing.T) {
	cases := []struct {
		label string
		state CurrencyState
		want  bool
	}{
		{"未启用", CurrencyState{Status: "disabled"}, false},
		{"实时汇率", CurrencyState{Status: "fresh", USDCNYRate: 7.2}, false},
		{"回退汇率", CurrencyState{Status: "fallback", USDCNYRate: 7.2}, true},
		{"已过期", CurrencyState{Status: "stale", USDCNYRate: 7.2}, true},
		{"缓存值但持续失败", CurrencyState{Status: "cached", USDCNYRate: 7.2, ConsecutiveFails: 3, Error: "timeout"}, true},
	}
	for _, tc := range cases {
		alerts := healthAlerts(StorageStatus{}, RuntimeStatus{ExchangeRate: tc.state})
		if got := hasHealthAlertCode(alerts, "exchange_rate_degraded"); got != tc.want {
			t.Fatalf("%s: 告警 = %v, want %v (alerts=%#v)", tc.label, got, tc.want, alerts)
		}
	}
}

func hasHealthAlertCode(alerts []HealthAlert, code string) bool {
	for _, alert := range alerts {
		if alert.Code == code {
			return true
		}
	}
	return false
}

func findModelStat(models []ModelStat, name string) (ModelStat, bool) {
	for _, model := range models {
		if model.Model == name {
			return model, true
		}
	}
	return ModelStat{}, false
}

// hasTimeBasedPricesLocked 走 priceVersion 派生缓存。这条测试保证缓存会随价格表的
// 每一次变动失效:漏一次就会让时段价格静默失效(加了规则却仍按基础价格算),或者
// 删除规则后仍走逐明细成本。
func TestTimeBasedPriceFlagFollowsEveryPriceMutation(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})

	assertFlag := func(label string, want bool) {
		t.Helper()
		stats.mu.Lock()
		got := stats.hasTimeBasedPricesLocked()
		stats.mu.Unlock()
		if got != want {
			t.Fatalf("%s: hasTimeBasedPrices = %v, want %v", label, got, want)
		}
	}

	assertFlag("空价格表", false)

	if _, err := stats.UpsertModelPrice("deepseek-chat", ModelPrice{Prompt: 6}); err != nil {
		t.Fatal(err)
	}
	assertFlag("仅基础价格", false)

	timed := ModelPrice{Prompt: 6, TimeRules: []ModelPriceRule{{Name: "peak", Start: "08:00", End: "09:00", Prompt: floatPtrForTest(12)}}}
	if _, err := stats.UpsertModelPrice("deepseek-chat", timed); err != nil {
		t.Fatal(err)
	}
	assertFlag("新增时段规则后", true)

	if _, err := stats.UpsertModelPrice("deepseek-chat", ModelPrice{Prompt: 6}); err != nil {
		t.Fatal(err)
	}
	assertFlag("覆盖为无规则价格后", false)

	if _, err := stats.UpsertModelPrice("deepseek-chat", timed); err != nil {
		t.Fatal(err)
	}
	assertFlag("再次写入规则后", true)

	if _, err := stats.DeleteModelPrice("deepseek-chat"); err != nil {
		t.Fatal(err)
	}
	assertFlag("删除该模型价格后", false)

	// models.dev 侧的价格表变动同样要让缓存失效。
	stats.mu.Lock()
	stats.modelsDevPrices = map[string]ModelPrice{"x": timed}
	stats.priceVersion++
	got := stats.hasTimeBasedPricesLocked()
	stats.mu.Unlock()
	if !got {
		t.Fatal("models.dev 价格表中的时段规则未被识别")
	}
}

// 落盘失败时内存价格表已经改了,版本号必须照常自增,否则派生缓存会继续用旧结论。
func TestPriceVersionAdvancesEvenWhenPersistFails(t *testing.T) {
	stats := NewRequestStatistics()
	dir := t.TempDir()
	// 把价格文件路径指向一个目录,写入必定失败。
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, PriceStoragePath: blocked})

	stats.mu.RLock()
	before := stats.priceVersion
	stats.mu.RUnlock()

	timed := ModelPrice{Prompt: 6, TimeRules: []ModelPriceRule{{Name: "peak", Start: "08:00", End: "09:00", Prompt: floatPtrForTest(12)}}}
	if _, err := stats.UpsertModelPrice("deepseek-chat", timed); err == nil {
		t.Skip("price persistence unexpectedly succeeded on this platform")
	}

	stats.mu.Lock()
	after := stats.priceVersion
	flag := stats.hasTimeBasedPricesLocked()
	stats.mu.Unlock()
	if after == before {
		t.Fatal("落盘失败后 priceVersion 未自增,派生缓存会读到过期结论")
	}
	if !flag {
		t.Fatal("落盘失败后内存价格表已含时段规则,时段价格标志应为 true")
	}
}

// 汇率 URL 来自用户配置,HTTPS 强制与重定向守卫是安全相关逻辑,必须直接覆盖。
func TestExchangeRateURLAndRedirectGuards(t *testing.T) {
	for _, raw := range []string{
		"http://open.er-api.com/v6/latest/USD",
		"ftp://example.com/rates.json",
		"https://",
		"/relative/path",
		"",
		"  ",
	} {
		if _, err := configureHTTPSURL(raw); err == nil {
			t.Fatalf("configureHTTPSURL(%q) accepted a non-HTTPS URL", raw)
		}
	}
	if _, err := configureHTTPSURL("  https://open.er-api.com/v6/latest/USD  "); err != nil {
		t.Fatalf("configureHTTPSURL rejected a valid HTTPS URL: %v", err)
	}

	httpsReq, err := http.NewRequest(http.MethodGet, "https://example.com/rates.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := exchangeRateCheckRedirect(httpsReq, nil); err != nil {
		t.Fatalf("HTTPS redirect rejected: %v", err)
	}
	plainReq, err := http.NewRequest(http.MethodGet, "http://example.com/rates.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := exchangeRateCheckRedirect(plainReq, nil); err == nil {
		t.Fatal("redirect downgrade to plain HTTP was allowed")
	}
	if err := exchangeRateCheckRedirect(httpsReq, make([]*http.Request, 10)); err == nil {
		t.Fatal("unbounded redirect chain was allowed")
	}
	if err := exchangeRateCheckRedirect(nil, nil); err == nil {
		t.Fatal("nil redirect request was allowed")
	}
}

// 非法 URL 不能覆盖已生效的配置,而且要留下可见的错误。
func TestInvalidExchangeRateURLKeepsPreviousValue(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{ExchangeRateEnabled: false, ExchangeRateUSD: "http://insecure.example.com/rates"})

	stats.mu.RLock()
	url, lastErr := stats.exchangeRateURL, stats.exchangeRateLastError
	stats.mu.RUnlock()
	if url != defaultExchangeRateURL {
		t.Fatalf("exchange rate URL = %q, want the previous %q", url, defaultExchangeRateURL)
	}
	if lastErr == "" {
		t.Fatal("rejecting a non-HTTPS exchange rate URL must be reported")
	}
}

// 汇率成功刷新必须推进 currencyVersion,否则看板的 summary ETag 不会失效,页面会一直
// 拿着旧汇率的缓存响应。
func TestExchangeRateRefreshAdvancesCurrencyVersion(t *testing.T) {
	stats := NewRequestStatistics()
	before := stats.CurrencyVersion()
	stats.recordExchangeRateFailure(errors.New("boom"))
	if stats.CurrencyVersion() == before {
		t.Fatal("拉取失败未推进 currencyVersion")
	}
	stats.mu.RLock()
	failures, lastErr := stats.exchangeRateFailures, stats.exchangeRateLastError
	stats.mu.RUnlock()
	if failures != 1 || lastErr != "boom" {
		t.Fatalf("failure state = %d, %q", failures, lastErr)
	}
}

// hasTimeBasedPricesLocked 会写派生缓存,因此只能在写锁下调用。这条测试让改价、
// 汇总查询、API 详情查询和事件查询并发跑,-race 下任何一个改成读锁路径都会报错。
func TestTimeBasedPriceCacheIsRaceFreeUnderConcurrentPricing(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 200, PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})
	base := recentRecordTime(stats.PricingLocation())
	for i := 0; i < 20; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-chat",
			RequestedAt: base.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{InputTokens: 100, OutputTokens: 50},
		})
	}

	start, end := hourWindowAround(t, base, stats.PricingLocation())
	timed := ModelPrice{Prompt: 6, TimeRules: []ModelPriceRule{{Name: "peak", Start: start, End: end, Prompt: floatPtrForTest(12)}}}
	plain := ModelPrice{Prompt: 6}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < 25; round++ {
				switch (worker + round) % 4 {
				case 0:
					price := plain
					if round%2 == 0 {
						price = timed
					}
					if _, err := stats.UpsertModelPrice("deepseek-chat", price); err != nil {
						t.Errorf("upsert: %v", err)
						return
					}
				case 1:
					stats.SummaryWithoutDetails()
				case 2:
					stats.QueryAPIDetail("deepseek", "24h", 10, 10)
				default:
					stats.QueryEvents(EventsQuery{Limit: 10})
				}
			}
		}(i)
	}
	wg.Wait()
}

// validateModelPriceRules 必须是纯校验(它曾在锁外写调用方的切片),入库和响应都必须
// 是深拷贝(否则调用方能在锁外改到存储里的价格)。
func TestModelPriceRuleValidationIsPureAndStorageIsDeepCopied(t *testing.T) {
	rules := []ModelPriceRule{{Name: "peak", Start: "08:00", End: "09:00", Prompt: floatPtrForTest(12)}}
	if err := validateModelPriceRules(rules); err != nil {
		t.Fatal(err)
	}
	if rules[0].ID != "" {
		t.Fatalf("校验不得改写调用方的规则,ID 被写成了 %q", rules[0].ID)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 10, PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})

	promptOverride := floatPtrForTest(12)
	caller := ModelPrice{Prompt: 6, TimeRules: []ModelPriceRule{{Name: "peak", Start: "08:00", End: "09:00", Prompt: promptOverride}}}
	response, err := stats.UpsertModelPrice("deepseek-chat", caller)
	if err != nil {
		t.Fatal(err)
	}
	if caller.TimeRules[0].ID != "" {
		t.Fatal("Upsert 不得改写调用方传入的规则")
	}
	stored := response.ManualPrices["deepseek-chat"]
	if len(stored.TimeRules) != 1 || strings.TrimSpace(stored.TimeRules[0].ID) == "" {
		t.Fatalf("入库规则应补上 ID: %#v", stored.TimeRules)
	}
	// 生效价格表同样会直接进 HTTP 响应,不能与存储共享底层数据。
	effective := response.Prices["deepseek-chat"]
	if len(effective.TimeRules) != 1 {
		t.Fatalf("生效价格缺少时段规则: %#v", effective)
	}
	if effective.TimeRules[0].Prompt == stored.TimeRules[0].Prompt {
		t.Fatal("Prices 与 ManualPrices 共享了同一个 *float64")
	}
	*effective.TimeRules[0].Prompt = 777

	// 调用方事后改动自己那份(以及响应那份),都不能影响存储里的价格。
	*promptOverride = 999
	caller.TimeRules[0].Start = "00:00"
	*stored.TimeRules[0].Prompt = 888
	stored.TimeRules[0].End = "23:00"

	stats.mu.RLock()
	live := stats.modelPrices["deepseek-chat"]
	stats.mu.RUnlock()
	if live.TimeRules[0].Prompt == nil || *live.TimeRules[0].Prompt != 12 {
		t.Fatalf("存储里的覆盖价被外部改动了: %v", live.TimeRules[0].Prompt)
	}
	if live.TimeRules[0].Start != "08:00" || live.TimeRules[0].End != "09:00" {
		t.Fatalf("存储里的时段被外部改动了: %#v", live.TimeRules[0])
	}
}

// 后台分页导出走的是 QueryExportEventsPage,不经过 queryEventsAt 的 finish()。
// 之前只测了 QueryEvents,导致「JSON/JSONL 无 cost_usd、CSV 整列为空」这条路径漏网。
func TestBackgroundPagedExportAttachesCosts(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})
	at := recentRecordTime(stats.PricingLocation())
	start, end := hourWindowAround(t, at, stats.PricingLocation())
	price := ModelPrice{
		Prompt:    6,
		TimeRules: []ModelPriceRule{{Name: "peak", Start: start, End: end, Prompt: floatPtrForTest(12)}},
	}
	if _, err := stats.UpsertModelPrice("deepseek-chat", price); err != nil {
		t.Fatalf("save price: %v", err)
	}
	for i := 0; i < 3; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-chat",
			RequestedAt: at.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{InputTokens: 1000},
		})
	}

	// 逐页翻,确认每一页(而不仅是第一页)都带上了成本。
	snapshot := time.Now()
	seen := 0
	for offset := 0; ; offset += 2 {
		page := stats.QueryExportEventsPage(EventsQuery{}, offset, 2, 0, snapshot, nil)
		if len(page.Events) == 0 {
			break
		}
		for _, event := range page.Events {
			if event.CostUSD == nil {
				t.Fatalf("offset %d: 后台导出缺失 cost_usd", offset)
			}
			// 1000 token @ 12/M 的峰值价。
			assertFloatNear(t, "exported cost", *event.CostUSD, 0.012)
			raw, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"cost_usd":`) {
				t.Fatalf("JSONL 导出缺失 cost_usd: %s", raw)
			}
			if got := dashboardEventCSVRecord(event); got[len(got)-1] == "" {
				t.Fatal("CSV 导出的 cost_usd 列为空")
			}
			seen++
		}
		if seen >= page.Total {
			break
		}
	}
	if seen != 3 {
		t.Fatalf("exported %d events, want 3", seen)
	}
}

// 图纸 172/204:「历史记录没有有效时间时,使用原有基础价,不因服务器当前时间套用峰谷
// 规则」。导入和存储恢复都会把零时间戳补成 now,effectivePrice 自己的 IsZero 守卫在
// 这两条路径上已经失效,必须靠 TimestampSynthetic 标记回落。
func TestRecordsWithoutTimestampKeepBasePriceAfterImport(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})

	// 时段规则覆盖「此刻」,这样一旦无时间戳记录被套上 now 就会算成峰值价。
	start, end := hourWindowAround(t, time.Now(), stats.PricingLocation())
	price := ModelPrice{
		Prompt:    6,
		TimeRules: []ModelPriceRule{{Name: "now-peak", Start: start, End: end, Prompt: floatPtrForTest(60)}},
	}
	if _, err := stats.UpsertModelPrice("deepseek-chat", price); err != nil {
		t.Fatalf("save price: %v", err)
	}

	stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"deepseek": {Models: map[string]ModelSnapshot{
				"deepseek-chat": {Details: []RequestDetail{{
					Model:    "deepseek-chat",
					Provider: "deepseek",
					Tokens:   TokenStats{InputTokens: 1000, TotalTokens: 1000},
					// 故意不给 Timestamp。
				}}},
			}},
		},
	})

	events := stats.QueryEvents(EventsQuery{Limit: 10})
	if len(events.Events) != 1 {
		t.Fatalf("events = %d", len(events.Events))
	}
	imported := events.Events[0]
	if !imported.TimestampSynthetic {
		t.Fatal("补出来的时间戳必须被标记为 synthetic")
	}
	if imported.Timestamp.IsZero() {
		t.Fatal("分桶/排序仍需要一个可用的时间戳")
	}
	if imported.CostUSD == nil {
		t.Fatal("missing cost_usd")
	}
	// 基础价 6/M × 1000 token,而不是峰值 60/M。
	assertFloatNear(t, "imported cost keeps the base price", *imported.CostUSD, 0.006)

	summary := stats.SummaryWithoutDetails()
	assertFloatNear(t, "summary total keeps the base price", summary.Usage.TotalCost, 0.006)

	// 恢复路径同样要打标记。
	restored := normalizeStorageSnapshotDetail("deepseek-chat", RequestDetail{Provider: "deepseek"}, time.Now())
	if !restored.TimestampSynthetic {
		t.Fatal("存储恢复补出的时间戳也必须标记为 synthetic")
	}
	withTime := normalizeStorageSnapshotDetail("deepseek-chat", RequestDetail{Provider: "deepseek", Timestamp: time.Now()}, time.Now())
	if withTime.TimestampSynthetic {
		t.Fatal("本来就有时间戳的记录不得被标记")
	}
}

// 后台分页导出会在页与页之间释放锁。价格必须在导出开始时冻结,否则同一个文件的前后
// 页会用上不同的价格。
func TestBackgroundExportFreezesPricesAcrossPages(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{MaxDetailsPerModel: 100, PriceStoragePath: filepath.Join(t.TempDir(), "prices.json")})
	at := recentRecordTime(stats.PricingLocation())
	if _, err := stats.UpsertModelPrice("deepseek-chat", ModelPrice{Prompt: 6}); err != nil {
		t.Fatalf("save price: %v", err)
	}
	for i := 0; i < 4; i++ {
		stats.Record(UsageRecord{
			Provider:    "deepseek",
			Model:       "deepseek-chat",
			RequestedAt: at.Add(time.Duration(i) * time.Minute),
			Detail:      UsageDetail{InputTokens: 1000},
		})
	}

	snapshotAt := time.Now()
	pricing := stats.PricingSnapshot()

	first := stats.QueryExportEventsPage(EventsQuery{}, 0, 2, 0, snapshotAt, pricing)
	if len(first.Events) != 2 {
		t.Fatalf("first page = %d events", len(first.Events))
	}

	// 翻页之间改价(等价于用户改价或 models.dev 自动刷新)。
	if _, err := stats.UpsertModelPrice("deepseek-chat", ModelPrice{Prompt: 600}); err != nil {
		t.Fatalf("reprice: %v", err)
	}

	second := stats.QueryExportEventsPage(EventsQuery{}, 2, 2, 0, snapshotAt, pricing)
	if len(second.Events) != 2 {
		t.Fatalf("second page = %d events", len(second.Events))
	}
	for i, event := range append(append([]RequestDetail{}, first.Events...), second.Events...) {
		if event.CostUSD == nil {
			t.Fatalf("event %d missing cost_usd", i)
		}
		assertFloatNear(t, fmt.Sprintf("event %d uses the frozen price", i), *event.CostUSD, 0.006)
	}

	// 不传快照时按当前价格计价,便于其他调用方沿用旧行为。
	live := stats.QueryExportEventsPage(EventsQuery{}, 0, 2, 0, snapshotAt, nil)
	if live.Events[0].CostUSD == nil {
		t.Fatal("missing cost_usd")
	}
	assertFloatNear(t, "no snapshot means current price", *live.Events[0].CostUSD, 0.6)
}

// 冷恢复重放 JSONL 是生产上最常走的一条补时间戳路径(每次开启存储的重启都会走)。
// 前面的用例只覆盖了 MergeSnapshot 和 normalizeStorageSnapshotDetail,漏掉了它。
func TestJSONLReplayKeepsBasePriceForRecordsWithoutTimestamp(t *testing.T) {
	dir := t.TempDir()
	storagePath := filepath.Join(dir, "usage.jsonl")

	// 一条没有 timestamp 字段的持久化记录,外加一条有真实时间戳的作为对照。对照记录
	// 的时刻和下面的时段窗口由同一个 withTime 推导,跨整点也不会失配。
	withTime := time.Now().In(mustLoadLocation(defaultPricingTimezone))
	lines := []string{
		`{"api":"deepseek","model":"deepseek-chat","detail":{"model":"deepseek-chat","provider":"deepseek","auth_index":"a1","source":"deepseek","tokens":{"input_tokens":1000,"total_tokens":1000}}}`,
		`{"api":"deepseek","model":"deepseek-chat","detail":{"model":"deepseek-chat","provider":"deepseek","auth_index":"a2","source":"deepseek","timestamp":"` +
			withTime.Format(time.RFC3339Nano) + `","tokens":{"input_tokens":1000,"total_tokens":1000}}}`,
	}
	if err := os.WriteFile(storagePath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats := NewRequestStatistics()
	stats.Configure(runtimeConfig{
		MaxDetailsPerModel: 100,
		StorageEnabled:     true,
		StoragePath:        storagePath,
		PriceStoragePath:   filepath.Join(dir, "prices.json"),
	})
	defer stats.Close()

	// 价格在冷恢复之后设置:事件成本是查询期附加的,顺序不影响结果,但必须晚于
	// Configure,否则会被 Configure 加载的价格文件覆盖掉。
	// 时段规则覆盖对照记录所在的整点:无时间戳记录一旦被套上恢复时刻,也会落进这个
	// 窗口而被算成峰值价 —— 这正是要防的。
	start, end := hourWindowAround(t, withTime, stats.PricingLocation())
	if _, err := stats.UpsertModelPrice("deepseek-chat", ModelPrice{
		Prompt:    6,
		TimeRules: []ModelPriceRule{{Name: "now-peak", Start: start, End: end, Prompt: floatPtrForTest(60)}},
	}); err != nil {
		t.Fatalf("save price: %v", err)
	}

	events := stats.QueryEvents(EventsQuery{Limit: 10})
	if len(events.Events) != 2 {
		t.Fatalf("replayed %d events, want 2", len(events.Events))
	}

	var synthetic, real *RequestDetail
	for i := range events.Events {
		if events.Events[i].TimestampSynthetic {
			synthetic = &events.Events[i]
		} else {
			real = &events.Events[i]
		}
	}
	if synthetic == nil {
		t.Fatal("JSONL 重放补出的时间戳必须被标记为 synthetic")
	}
	if real == nil {
		t.Fatal("本来就有时间戳的记录不得被标记")
	}
	if synthetic.Timestamp.IsZero() {
		t.Fatal("分桶/排序仍需要一个可用的时间戳")
	}
	if synthetic.CostUSD == nil || real.CostUSD == nil {
		t.Fatal("missing cost_usd")
	}
	// 无时间戳记录走基础价 6/M;有真实时间戳且落在规则内的走峰值 60/M。
	assertFloatNear(t, "replayed record without timestamp keeps the base price", *synthetic.CostUSD, 0.006)
	assertFloatNear(t, "replayed record with a real timestamp uses the peak price", *real.CostUSD, 0.06)
}

// 2026-08-22 是周六,08-24 是周一。用同一个 UTC 时刻配 Asia/Shanghai,验证星期
// 判定发生在计费时区里,而不是 UTC 里。
func TestPricingRuleDaysRestrictWeekdays(t *testing.T) {
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	workdays := ModelPrice{Prompt: 10, TimeRules: []ModelPriceRule{{
		Name: "workday-peak", Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "09:00", Prompt: floatPtrForTest(2),
	}}}
	if err := validateModelPriceRules(workdays.TimeRules); err != nil {
		t.Fatalf("工作日规则应通过校验: %v", err)
	}

	// 周一 08:30(Shanghai)= 周一 00:30 UTC
	monday := time.Date(2026, 8, 24, 0, 30, 0, 0, time.UTC)
	if got := effectivePrice(workdays, monday, shanghai).Prompt; got != 2 {
		t.Fatalf("周一峰值价 = %v, want 2", got)
	}
	// 周六 08:30(Shanghai)= 周六 00:30 UTC,同一个钟点但不在工作日内
	saturday := time.Date(2026, 8, 22, 0, 30, 0, 0, time.UTC)
	if got := effectivePrice(workdays, saturday, shanghai).Prompt; got != 10 {
		t.Fatalf("周六应回落基础价, got %v, want 10", got)
	}
	// 同一个 UTC 时刻在 UTC 里是周五 17:30/周六,星期必须按计费时区判。
	if got := effectivePrice(workdays, monday, time.UTC).Prompt; got != 10 {
		t.Fatalf("UTC 下 00:30 不在 08:00-09:00 内, got %v, want 10", got)
	}
}

// 星期不相交时,同一时间段必须允许存在两条不同价格的规则——「工作日夜间半价、
// 周末夜间原价」是这个功能的主要用途,旧的纯时间重叠校验会把它判成冲突。
func TestPricingRulesWithDisjointDaysMayShareTimeRange(t *testing.T) {
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	price := ModelPrice{Prompt: 10, TimeRules: []ModelPriceRule{
		{Name: "workday-night", Days: []int{1, 2, 3, 4, 5}, Start: "22:00", End: "06:00", Prompt: floatPtrForTest(5)},
		{Name: "weekend-night", Days: []int{0, 6}, Start: "22:00", End: "06:00", Prompt: floatPtrForTest(20)},
	}}
	if err := validateModelPriceRules(price.TimeRules); err != nil {
		t.Fatalf("星期不相交的同时段规则不应判为重叠: %v", err)
	}

	// 周一 23:00 Shanghai = 周一 15:00 UTC
	if got := effectivePrice(price, time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC), shanghai).Prompt; got != 5 {
		t.Fatalf("工作日夜间价 = %v, want 5", got)
	}
	// 周六 23:00 Shanghai = 周六 15:00 UTC
	if got := effectivePrice(price, time.Date(2026, 8, 22, 15, 0, 0, 0, time.UTC), shanghai).Prompt; got != 20 {
		t.Fatalf("周末夜间价 = %v, want 20", got)
	}
	// 跨午夜规则按请求实际落在的那一天判:周六 02:00 归周末,即使区间起点是周五 22:00。
	if got := effectivePrice(price, time.Date(2026, 8, 21, 18, 0, 0, 0, time.UTC), shanghai).Prompt; got != 20 {
		t.Fatalf("周六凌晨应按周末价, got %v, want 20", got)
	}
	// 星期相交后仍必须报重叠。
	conflicting := append([]ModelPriceRule(nil), price.TimeRules...)
	conflicting[1].Days = []int{5, 6}
	if err := validateModelPriceRules(conflicting); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("星期相交时应报重叠, got %v", err)
	}
}

func TestPricingRuleDaysValidationAndNormalization(t *testing.T) {
	for name, days := range map[string][]int{
		"越界的星期":  {7},
		"负数星期":   {-1},
		"重复的星期":  {1, 1},
		"超过七个元素": {0, 1, 2, 3, 4, 5, 6, 0},
	} {
		rules := []ModelPriceRule{{Name: "r", Days: days, Start: "08:00", End: "09:00", Prompt: floatPtrForTest(1)}}
		if err := validateModelPriceRules(rules); err == nil {
			t.Fatalf("%s 应被拒绝: %v", name, days)
		}
	}

	// 缺省 days 保持「每天」语义,导出里不出现该字段。
	legacy := normalizeModelPriceRules(ModelPrice{Prompt: 1, TimeRules: []ModelPriceRule{{Name: "all", Start: "08:00", End: "09:00", Prompt: floatPtrForTest(1)}}})
	if legacy.TimeRules[0].Days != nil {
		t.Fatalf("旧规则不应被补出 days: %#v", legacy.TimeRules[0].Days)
	}
	raw, err := json.Marshal(legacy.TimeRules[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "days") {
		t.Fatalf("每天的规则不应序列化出 days 字段: %s", raw)
	}

	// 覆盖整周 == 每天,收敛成 nil;乱序/重复被排序去重。
	normalized := normalizeModelPriceRules(ModelPrice{Prompt: 1, TimeRules: []ModelPriceRule{
		{Name: "full", Days: []int{6, 5, 4, 3, 2, 1, 0}, Start: "08:00", End: "09:00", Prompt: floatPtrForTest(1)},
		{Name: "messy", Days: []int{5, 1, 5}, Start: "10:00", End: "11:00", Prompt: floatPtrForTest(1)},
	}})
	if normalized.TimeRules[0].Days != nil {
		t.Fatalf("整周应收敛成 nil: %#v", normalized.TimeRules[0].Days)
	}
	if got := normalized.TimeRules[1].Days; len(got) != 2 || got[0] != 1 || got[1] != 5 {
		t.Fatalf("days 应排序去重, got %#v", got)
	}

	// 深拷贝:调用方改自己的切片不能影响归一化后的副本。
	caller := []int{1, 2}
	source := ModelPrice{Prompt: 1, TimeRules: []ModelPriceRule{{Name: "r", Days: caller, Start: "08:00", End: "09:00", Prompt: floatPtrForTest(1)}}}
	copied := normalizeModelPriceRules(source)
	caller[0] = 6
	if copied.TimeRules[0].Days[0] != 1 {
		t.Fatalf("days 未深拷贝: %#v", copied.TimeRules[0].Days)
	}
}

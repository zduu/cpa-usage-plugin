package main

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"time"
)

func quotaProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "codex", "devin", "antigravity":
		return true
	}
	return false
}

func quotaCredentialKey(provider, index, id string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "\x00" + index + "\x00" + id
}

func quotaWindowKey(o quotaObservation) string {
	return quotaCredentialKey(o.Provider, o.AuthIndex, o.AuthID) + "\x00" + o.Group + "\x00" + o.Slot
}

func quotaNumber(raw string, scale float64) (float64, bool) {
	x, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%")), 64)
	return x / scale, err == nil && !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x <= scale
}

func quotaTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if unix, err := strconv.ParseInt(raw, 10, 64); err == nil && unix > 0 && unix < 253402300800 {
		return time.Unix(unix, 0).UTC()
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || t.Year() < 1970 || t.Year() > 9999 {
		return time.Time{}
	}
	return t.UTC()
}

func parseQuotaSignals(input quotaSignalsInput) []quotaObservation {
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	if !quotaProvider(provider) || input.ObservedAt.IsZero() || (input.AuthIndex == "" && input.AuthID == "") {
		return nil
	}
	signals := make(map[string]string, len(input.Signals))
	for key, value := range input.Signals {
		if len(key) > 256 || len(value) > 512 || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			continue
		}
		signals[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	var out []quotaObservation
	revoke := func(group, slot string) {
		out = append(out, quotaObservation{Provider: provider, AuthIndex: input.AuthIndex, AuthID: input.AuthID,
			Group: group, Slot: slot, ObservedAt: input.ObservedAt.UTC(), Revoked: true})
	}
	add := func(group, name, slot string, seconds int64, used float64, reset time.Time, unmapped bool) {
		if reset.IsZero() || seconds <= 0 || seconds > 366*86400 {
			return
		}
		o := quotaObservation{Provider: provider, AuthIndex: input.AuthIndex, AuthID: input.AuthID,
			Group: group, Name: name, Slot: slot, Seconds: seconds, Used: used, Reset: reset,
			ObservedAt: input.ObservedAt.UTC(), Unmapped: unmapped}
		if validQuotaObservation(o) {
			out = append(out, o)
		}
	}
	switch provider {
	case "antigravity":
		if len(input.AntigravityBuckets) > 64 {
			return nil
		}
		for _, bucket := range input.AntigravityBuckets {
			bucket.Group, bucket.ID = strings.TrimSpace(bucket.Group), strings.TrimSpace(bucket.ID)
			if bucket.Group == "" || len(bucket.Group) > 256 || len(bucket.ID) > 256 || len(bucket.Window) > 64 || len(bucket.ResetTime) > 128 || strings.IndexFunc(bucket.Group+bucket.ID, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 || bucket.RemainingFraction == nil {
				continue
			}
			remaining := *bucket.RemainingFraction
			if math.IsNaN(remaining) || math.IsInf(remaining, 0) || remaining < 0 || remaining > 1 {
				continue
			}
			var seconds int64
			switch strings.ToLower(strings.TrimSpace(bucket.Window)) {
			case "5h", "five-hour", "five_hour":
				seconds = 18000
			case "weekly", "week":
				seconds = 604800
			default:
				continue // A reset timestamp alone does not define a period.
			}
			groupHash := sha256.Sum256([]byte(bucket.Group))
			bucketHash := sha256.Sum256([]byte(bucket.ID + "\x00" + strconv.FormatInt(seconds, 10)))
			add("antigravity-"+hex.EncodeToString(groupHash[:16]), bucket.Group, hex.EncodeToString(bucketHash[:16]), seconds,
				1-remaining, quotaTime(bucket.ResetTime), true)
		}
	case "claude":
		for _, w := range []struct {
			slot    string
			seconds int64
		}{{"5h", 18000}, {"7d", 604800}} {
			prefix := "anthropic-ratelimit-unified-" + w.slot + "-"
			if signals[prefix+"utilization"] == "null" {
				revoke("shared", w.slot)
				continue
			}
			if used, ok := quotaNumber(signals[prefix+"utilization"], 1); ok {
				add("shared", "", w.slot, w.seconds, used, quotaTime(signals[prefix+"reset"]), false)
			}
		}
	case "codex":
		for key, raw := range signals {
			if !strings.HasPrefix(key, "x-codex-") || !strings.HasSuffix(key, "-used-percent") {
				continue
			}
			prefix := strings.TrimSuffix(key, "used-percent")
			slot := ""
			for _, candidate := range []string{"primary", "secondary"} {
				if strings.HasSuffix(prefix, candidate+"-") {
					slot = candidate
					break
				}
			}
			if slot == "" {
				continue
			}
			namespace := strings.TrimSuffix(strings.TrimPrefix(prefix, "x-codex-"), slot+"-")
			group, name, unmapped := "shared", "", false
			if namespace != "" {
				group, name, unmapped = strings.TrimSuffix(namespace, "-"), strings.TrimSuffix(namespace, "-"), true
				if label := signals["x-codex-"+namespace+"limit-name"]; label != "" {
					group, name = strings.ToLower(label), label
				}
			}
			if raw == "null" {
				revoke(group, slot)
				continue
			}
			used, ok := quotaNumber(raw, 100)
			minutes, err := strconv.ParseInt(signals[prefix+"window-minutes"], 10, 64)
			if !ok || err != nil || minutes <= 0 || minutes > int64(math.MaxInt64)/int64(time.Minute) {
				continue
			}
			reset := quotaTime(signals[prefix+"reset-at"])
			if reset.IsZero() {
				delta, err := strconv.ParseInt(signals[prefix+"reset-after-seconds"], 10, 64)
				if err == nil && delta >= 0 && delta <= int64(math.MaxInt64)/int64(time.Second) {
					reset = input.ObservedAt.Add(time.Duration(delta) * time.Second)
				}
			}
			add(group, name, slot, minutes*60, used, reset, unmapped)
		}
	case "devin":
		for _, w := range []struct {
			slot    string
			seconds int64
		}{{"daily", 86400}, {"weekly", 604800}} {
			if signals[w.slot+"_quota_remaining_percent"] == "null" {
				revoke("shared", w.slot)
				continue
			}
			if remaining, ok := quotaNumber(signals[w.slot+"_quota_remaining_percent"], 100); ok {
				add("shared", "", w.slot, w.seconds, 1-remaining, quotaTime(signals[w.slot+"_quota_reset_at"]), false)
			}
		}
	}
	return out
}

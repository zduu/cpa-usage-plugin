package main

import (
	"strings"
)

// ============================================================================
// API Key Detection & Masking
// ============================================================================

// looksLikeSecretKey returns true when raw looks like an API key rather than
// a human-readable identifier.
func looksLikeSecretKey(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "sk-") || strings.HasPrefix(s, "AIza") ||
		strings.HasPrefix(s, "hf_") || strings.HasPrefix(s, "pk_") ||
		strings.HasPrefix(s, "rk_") {
		return true
	}
	if len(s) >= 40 && !strings.ContainsAny(s, " /.-_") {
		return true
	}
	if len(s) >= 80 && !strings.Contains(s, " ") {
		return true
	}
	return false
}

func maskAPIKey(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return s[:1] + "******"
	}
	prefix := 2
	suffix := 2
	if len(s) < prefix+suffix {
		return s[:1] + "******" + s[len(s)-1:]
	}
	return s[:prefix] + "******" + s[len(s)-suffix:]
}

func stripCredentialSuffix(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parts := splitBySeparators(value)
	for i, part := range parts {
		normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(part, "-", ""), "_", "")))
		if normalized == "apikey" || normalized == "key" || normalized == "credential" || normalized == "auth" {
			if i > 0 {
				return strings.Join(parts[:i], " · ")
			}
		}
	}
	if len(parts) > 1 && looksLikeCredentialID(parts[len(parts)-1]) {
		return strings.Join(parts[:len(parts)-1], " · ")
	}
	if len(parts) > 1 && looksLikeSecretKey(parts[len(parts)-1]) {
		return strings.Join(parts[:len(parts)-1], " · ")
	}
	colonParts := strings.Split(value, ":")
	if len(colonParts) >= 3 && looksLikeCredentialID(colonParts[len(colonParts)-1]) {
		return strings.Join(colonParts[:len(colonParts)-1], ":")
	}
	return value
}

// splitBySeparators splits s on " · ", " - ", " | ", or "/" in priority order.
func splitBySeparators(s string) []string {
	if strings.Contains(s, " · ") {
		return strings.Split(s, " · ")
	}
	if strings.Contains(s, " - ") {
		return strings.Split(s, " - ")
	}
	if strings.Contains(s, " | ") {
		return strings.Split(s, " | ")
	}
	if strings.Contains(s, "/") {
		return strings.Split(s, "/")
	}
	return []string{s}
}

func looksLikeCredentialID(raw string) bool {
	s := strings.TrimSpace(raw)
	if len(s) >= 8 {
		allHex := true
		for _, ch := range s {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				allHex = false
				break
			}
		}
		if allHex {
			return true
		}
	}
	return len(s) >= 32 && !strings.ContainsAny(s, " /.-_")
}

// ============================================================================
// Source / Group Key Helpers
// ============================================================================

func friendlySourceName(record UsageRecord) string {
	provider := strings.TrimSpace(record.Provider)
	executor := strings.TrimSpace(record.ExecutorType)
	source := stripCredentialSuffix(record.Source)

	if source != "" && !looksLikeSecretKey(source) {
		return source
	}
	name := provider
	if name == "" {
		name = executor
	}
	if name == "" {
		name = "unknown"
	}
	return stripCredentialSuffix(name)
}

func usageGroupKey(record UsageRecord) string {
	provider := strings.TrimSpace(record.Provider)
	executor := strings.TrimSpace(record.ExecutorType)
	source := stripCredentialSuffix(record.Source)

	parts := make([]string, 0, 3)
	if provider != "" {
		parts = append(parts, provider)
	} else if executor != "" {
		parts = append(parts, executor)
	}
	if source != "" && !looksLikeSecretKey(source) {
		dup := false
		for _, p := range parts {
			if p == source {
				dup = true
				break
			}
		}
		if !dup {
			parts = append(parts, source)
		}
	}
	if len(parts) == 0 {
		return "未知接口"
	}
	return strings.Join(parts, " · ")
}

func usageSource(record UsageRecord) string {
	return friendlySourceName(record)
}

func usageThinking(record UsageRecord) UsageThinking {
	effort := strings.TrimSpace(record.ReasoningEffort)
	if effort == "" {
		return UsageThinking{}
	}
	return UsageThinking{Intensity: effort, Level: effort}
}

// ============================================================================
// Small Utilities
// ============================================================================

func trimLong(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ============================================================================
// Response Header Filtering
// ============================================================================

func parseHeaderWhitelist(raw string) map[string]bool {
	set := make(map[string]bool)
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		set[strings.ToLower(name)] = true
	}
	return set
}

func filterHeaders(headers map[string][]string, whitelist map[string]bool) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	if len(whitelist) == 0 {
		return nil
	}
	if whitelist["*"] {
		out := make(map[string][]string, len(headers))
		for k, v := range headers {
			vv := make([]string, len(v))
			copy(vv, v)
			out[k] = vv
		}
		return out
	}
	out := make(map[string][]string)
	for k, v := range headers {
		if whitelist[strings.ToLower(k)] {
			vv := make([]string, len(v))
			copy(vv, v)
			out[k] = vv
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ============================================================================
// Sensitive Text Redaction
// ============================================================================

const redactedMarker = "******"

func redactSensitiveText(value string) string {
	if value == "" {
		return ""
	}
	value = redactKeyPrefix(value, "sk-")
	value = redactKeyPrefix(value, "AIza")
	value = redactKeyPrefix(value, "hf_")
	value = redactKeyPrefix(value, "pk_")
	value = redactKeyPrefix(value, "rk_")

	value = redactAuthHeader(value, "Authorization:")
	value = redactAuthHeader(value, "authorization:")
	value = redactAuthHeader(value, "Bearer ")
	value = redactAuthHeader(value, "bearer ")

	value = redactAuthHeader(value, "X-API-Key:")
	value = redactAuthHeader(value, "x-api-key:")
	value = redactAuthHeader(value, "Api-Key:")
	value = redactAuthHeader(value, "api-key:")

	value = redactQueryParam(value, "key")
	value = redactQueryParam(value, "token")
	value = redactQueryParam(value, "api_key")
	value = redactQueryParam(value, "apikey")

	return value
}

func redactKeyPrefix(s, prefix string) string {
	result := s
	for {
		idx := strings.Index(result, prefix)
		if idx < 0 {
			break
		}
		end := strings.IndexFunc(result[idx+len(prefix):], func(r rune) bool {
			return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_'
		})
		var token string
		if end < 0 {
			token = result[idx:]
		} else {
			token = result[idx : idx+len(prefix)+end]
		}
		masked := maskToken(token)
		result = strings.Replace(result, token, masked, 1)
	}
	return result
}

func redactAuthHeader(s, marker string) string {
	idx := strings.Index(s, marker)
	if idx < 0 {
		return s
	}
	rest := s[idx+len(marker):]
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return s
	}
	end := strings.IndexAny(rest, " ,\n\r;")
	var token string
	if end < 0 {
		token = rest
	} else {
		token = rest[:end]
	}
	if len(token) == 0 {
		return s
	}
	if !looksLikeSecretToken(token) {
		return s
	}
	masked := maskToken(token)
	return strings.Replace(s, marker+" "+token, marker+" "+masked, 1)
}

func redactQueryParam(s, param string) string {
	prefixes := []string{param + "=", param + " %3D ", param + "%3D"}
	for _, prefix := range prefixes {
		idx := strings.Index(s, prefix)
		if idx < 0 {
			continue
		}
		afterIdx := idx + len(prefix)
		rest := s[afterIdx:]
		end := strings.IndexAny(rest, " &;\n\r")
		var value string
		if end < 0 {
			value = rest
		} else {
			value = rest[:end]
		}
		if len(value) > 0 && looksLikeSecretToken(value) {
			s = s[:afterIdx] + redactedMarker + s[afterIdx+len(value):]
		}
	}
	return s
}

func looksLikeSecretToken(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return false
	}
	for _, p := range []string{"sk-", "AIza", "hf_", "pk_", "rk_"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	if len(s) >= 32 && !strings.Contains(s, " ") {
		return true
	}
	return false
}

func maskToken(token string) string {
	if len(token) <= 4 {
		return redactedMarker
	}
	show := 2
	return token[:show] + redactedMarker + token[len(token)-show:]
}

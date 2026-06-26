package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const pluginVersion = "1.2.8"

func handleRegister(requestBody []byte) ([]byte, error) {
	applyRuntimeConfig(requestBody)

	result := PluginRegisterResponse{
		SchemaVersion: 1,
		Metadata: PluginMetadata{
			Name:             "用量统计",
			Version:          pluginVersion,
			Author:           "本地维护",
			GitHubRepository: "https://github.com/zduu/cpa-usage-plugin",
			Logo:             "",
			ConfigFields: []ConfigField{
				{
					Name:        "max_details_per_model",
					Type:        "integer",
					Default:     defaultMaxDetailsPerModel,
					Description: "每个上游接口/模型最多保留的请求明细条数。",
				},
				{
					Name:        "retention_days",
					Type:        "integer",
					Default:     defaultRetentionDays,
					Description: "内存统计最多保留的天数，0 表示不按时间淘汰。",
				},
				{
					Name:        "dedup_window_minutes",
					Type:        "integer",
					Default:     defaultDedupWindowMinutes,
					Description: "usage 记录去重窗口分钟数，0 表示关闭去重。",
				},
				{
					Name:        "log_response_headers",
					Type:        "string",
					Default:     "",
					Description: "允许记录的响应头名称列表（逗号分隔），支持 * 通配符。留空不记录任何响应头。",
				},
				{
					Name:        "api_key_hash_salt",
					Type:        "string",
					Default:     "",
					Description: "可选：用于 API key 分组哈希的稳定 salt。留空时使用进程内随机 salt。",
				},
				{
					Name:        "storage_enabled",
					Type:        "boolean",
					Default:     false,
					Description: "是否启用 JSONL 事件持久化。默认关闭。",
				},
				{
					Name:        "storage_path",
					Type:        "string",
					Default:     "usage-statistics.jsonl",
					Description: "JSONL 持久化文件路径。相对路径基于 CPA 工作目录。",
				},
				{
					Name:        "storage_flush_interval_seconds",
					Type:        "integer",
					Default:     defaultStorageFlushSeconds,
					Description: "持久化文件 flush 间隔秒数。",
				},
				{
					Name:        "price_storage_path",
					Type:        "string",
					Default:     defaultPriceStoragePath,
					Description: "模型价格表 JSON 文件路径。相对路径基于 CPA 工作目录。",
				},
				{
					Name:        "update_enabled",
					Type:        "boolean",
					Default:     false,
					Description: "是否允许外部更新脚本拉取 GitHub Release 并替换插件文件。替换后需要手动重启 CPA。",
				},
				{
					Name:        "update_version",
					Type:        "string",
					Default:     "latest",
					Description: "更新目标版本。latest 表示最新 Release；也可填写 v1.1.0 这类固定版本。",
				},
			},
		},
		Capabilities: PluginCapabilities{
			UsagePlugin:   true,
			ManagementAPI: true,
		},
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}

	return okEnvelopeJSON(string(resultJSON))
}

func handleReconfigure(requestBody []byte) ([]byte, error) {
	return handleRegister(requestBody)
}

func applyRuntimeConfig(requestBody []byte) {
	stats.ConfigurePatch(parseRuntimeConfigPatch(requestBody))
}

func parseRuntimeConfig(requestBody []byte) runtimeConfig {
	cfg := defaultRuntimeConfig()
	patch := parseRuntimeConfigPatch(requestBody)
	if patch.MaxDetailsPerModel != nil {
		cfg.MaxDetailsPerModel = *patch.MaxDetailsPerModel
	}
	if patch.RetentionDays != nil {
		cfg.RetentionDays = *patch.RetentionDays
	}
	if patch.DedupWindowMinutes != nil {
		cfg.DedupWindowMinutes = *patch.DedupWindowMinutes
	}
	if patch.LogResponseHeaders != nil {
		cfg.LogResponseHeaders = *patch.LogResponseHeaders
	}
	if patch.APIKeyHashSalt != nil {
		cfg.APIKeyHashSalt = *patch.APIKeyHashSalt
	}
	if patch.StorageEnabled != nil {
		cfg.StorageEnabled = *patch.StorageEnabled
	}
	if patch.StoragePath != nil {
		cfg.StoragePath = *patch.StoragePath
	}
	if patch.StorageFlushSeconds != nil {
		cfg.StorageFlushSeconds = *patch.StorageFlushSeconds
	}
	if patch.PriceStoragePath != nil {
		cfg.PriceStoragePath = *patch.PriceStoragePath
	}
	if patch.UpdateEnabled != nil {
		cfg.UpdateEnabled = *patch.UpdateEnabled
	}
	if patch.UpdateVersion != nil {
		cfg.UpdateVersion = *patch.UpdateVersion
	}
	return cfg
}

func parseRuntimeConfigPatch(requestBody []byte) runtimeConfigPatch {
	defaults := defaultRuntimeConfig()
	patch := runtimeConfigPatch{
		MaxDetailsPerModel:  intPtr(defaults.MaxDetailsPerModel),
		RetentionDays:       intPtr(defaults.RetentionDays),
		DedupWindowMinutes:  intPtr(defaults.DedupWindowMinutes),
		LogResponseHeaders:  stringPtr(defaults.LogResponseHeaders),
		APIKeyHashSalt:      stringPtr(defaults.APIKeyHashSalt),
		StorageEnabled:      boolPtr(defaults.StorageEnabled),
		StoragePath:         stringPtr(defaults.StoragePath),
		StorageFlushSeconds: intPtr(defaults.StorageFlushSeconds),
		PriceStoragePath:    stringPtr(defaults.PriceStoragePath),
		UpdateEnabled:       boolPtr(defaults.UpdateEnabled),
		UpdateVersion:       stringPtr(defaults.UpdateVersion),
	}
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if len(requestBody) == 0 || json.Unmarshal(requestBody, &req) != nil || len(req.ConfigYAML) == 0 {
		return patch
	}
	values := usageStatisticsConfigValues(req.ConfigYAML)
	if v, ok := intConfig(values, "max_details_per_model"); ok {
		patch.MaxDetailsPerModel = intPtr(v)
	}
	if v, ok := intConfig(values, "retention_days"); ok {
		patch.RetentionDays = intPtr(v)
	}
	if v, ok := intConfig(values, "dedup_window_minutes"); ok {
		patch.DedupWindowMinutes = intPtr(v)
	}
	if s, ok := stringConfig(values, "log_response_headers"); ok {
		patch.LogResponseHeaders = stringPtr(s)
	}
	if s, ok := stringConfig(values, "api_key_hash_salt"); ok {
		patch.APIKeyHashSalt = stringPtr(s)
	}
	if v, ok := boolConfig(values, "storage_enabled"); ok {
		patch.StorageEnabled = boolPtr(v)
	}
	if s, ok := stringConfig(values, "storage_path"); ok {
		patch.StoragePath = stringPtr(s)
	}
	if v, ok := intConfig(values, "storage_flush_interval_seconds"); ok {
		patch.StorageFlushSeconds = intPtr(v)
	}
	if s, ok := stringConfig(values, "price_storage_path"); ok {
		patch.PriceStoragePath = stringPtr(s)
	}
	if v, ok := boolConfig(values, "update_enabled"); ok {
		patch.UpdateEnabled = boolPtr(v)
	}
	if s, ok := stringConfig(values, "update_version"); ok && s != "" {
		patch.UpdateVersion = stringPtr(s)
	}
	return patch
}

func usageStatisticsConfigValues(yamlBytes []byte) map[string]interface{} {
	var root map[string]interface{}
	if err := yaml.Unmarshal(yamlBytes, &root); err != nil {
		return nil
	}
	if values, ok := nestedMap(root, "plugins", "configs", "usage-statistics"); ok {
		return values
	}
	if values, ok := nestedMap(root, "configs", "usage-statistics"); ok {
		return values
	}
	if values, ok := nestedMap(root, "usage-statistics"); ok {
		return values
	}
	return root
}

func nestedMap(root map[string]interface{}, path ...string) (map[string]interface{}, bool) {
	current := root
	for _, key := range path {
		value, ok := current[key]
		if !ok {
			return nil, false
		}
		next, ok := value.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func intConfig(values map[string]interface{}, key string) (int, bool) {
	value, ok := values[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int:
		if v >= 0 {
			return v, true
		}
	case int64:
		if v >= 0 {
			return int(v), true
		}
	case float64:
		if v >= 0 && v == float64(int(v)) {
			return int(v), true
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil && parsed >= 0 {
			return parsed, true
		}
	}
	return 0, false
}

func stringConfig(values map[string]interface{}, key string) (string, bool) {
	value, ok := values[key]
	if !ok {
		return "", false
	}
	return strings.TrimSpace(fmt.Sprint(value)), true
}

func boolConfig(values map[string]interface{}, key string) (bool, bool) {
	value, ok := values[key]
	if !ok {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "on", "1":
			return true, true
		case "false", "no", "off", "0":
			return false, true
		}
	}
	return false, false
}

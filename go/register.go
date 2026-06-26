package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

func handleRegister(requestBody []byte) ([]byte, error) {
	applyRuntimeConfig(requestBody)

	result := PluginRegisterResponse{
		SchemaVersion: 1,
		Metadata: PluginMetadata{
			Name:             "用量统计",
			Version:          "1.2.6",
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
	stats.Configure(parseRuntimeConfig(requestBody))
}

func parseRuntimeConfig(requestBody []byte) runtimeConfig {
	cfg := defaultRuntimeConfig()
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if len(requestBody) == 0 || json.Unmarshal(requestBody, &req) != nil || len(req.ConfigYAML) == 0 {
		return cfg
	}
	yamlText := string(req.ConfigYAML)
	cfg.MaxDetailsPerModel = yamlInt(yamlText, "max_details_per_model", cfg.MaxDetailsPerModel)
	cfg.RetentionDays = yamlInt(yamlText, "retention_days", cfg.RetentionDays)
	cfg.DedupWindowMinutes = yamlInt(yamlText, "dedup_window_minutes", cfg.DedupWindowMinutes)
	if s := yamlString(yamlText, "log_response_headers"); s != "" {
		cfg.LogResponseHeaders = s
	}
	cfg.UpdateEnabled = yamlBool(yamlText, "update_enabled", cfg.UpdateEnabled)
	if s := yamlString(yamlText, "update_version"); s != "" {
		cfg.UpdateVersion = s
	}
	return cfg
}

func yamlInt(yamlText, key string, fallback int) int {
	for _, line := range strings.Split(yamlText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix := key + ":"
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		if idx > 0 && line[idx-1] != ' ' && line[idx-1] != '\t' {
			continue
		}
		value := strings.TrimSpace(line[idx+len(prefix):])
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if value == "" {
			return fallback
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return fallback
		}
		return parsed
	}
	return fallback
}

func yamlString(yamlText, key string) string {
	for _, line := range strings.Split(yamlText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix := key + ":"
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		if idx > 0 && line[idx-1] != ' ' && line[idx-1] != '\t' {
			continue
		}
		value := strings.TrimSpace(line[idx+len(prefix):])
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		return strings.TrimSpace(value)
	}
	return ""
}

func yamlBool(yamlText, key string, fallback bool) bool {
	value := strings.ToLower(yamlString(yamlText, key))
	switch value {
	case "true", "yes", "on", "1":
		return true
	case "false", "no", "off", "0":
		return false
	default:
		return fallback
	}
}

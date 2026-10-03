package main

import "time"

// Quota facts retain only the dimensions needed for period accounting and repricing.
type quotaFact struct {
	ID                 string     `json:"id"`
	API                string     `json:"api"`
	Provider           string     `json:"provider"`
	AuthIndex          string     `json:"auth_index"`
	AuthID             string     `json:"auth_id"`
	Model              string     `json:"model"`
	Timestamp          time.Time  `json:"timestamp"`
	TimestampSynthetic bool       `json:"timestamp_synthetic,omitempty"`
	CompletedAt        time.Time  `json:"completed_at,omitempty"`
	Failed             bool       `json:"failed"`
	Tokens             TokenStats `json:"tokens"`
}

type quotaObservation struct {
	Provider   string    `json:"provider"`
	AuthIndex  string    `json:"auth_index"`
	AuthID     string    `json:"auth_id"`
	Group      string    `json:"group"`
	Name       string    `json:"name"`
	Slot       string    `json:"slot"`
	Seconds    int64     `json:"seconds"`
	Reset      time.Time `json:"reset"`
	ObservedAt time.Time `json:"observed_at"`
	Used       float64   `json:"used"`
	Model      string    `json:"model,omitempty"`
	Unmapped   bool      `json:"unmapped,omitempty"`
	Revoked    bool      `json:"revoked,omitempty"`
}

type quotaPeriod struct {
	Start   time.Time          `json:"start"`
	End     time.Time          `json:"end"`
	Samples []quotaObservation `json:"samples"`
}

type quotaWindow struct {
	Provider        string       `json:"provider"`
	AuthIndex       string       `json:"auth_index"`
	AuthID          string       `json:"auth_id"`
	Group           string       `json:"group"`
	Name            string       `json:"name"`
	Slot            string       `json:"slot"`
	Seconds         int64        `json:"seconds"`
	Model           string       `json:"model,omitempty"`
	Unmapped        bool         `json:"unmapped,omitempty"`
	Hidden          bool         `json:"hidden,omitempty"`
	UpdatedAt       time.Time    `json:"updated_at"`
	Current         *quotaPeriod `json:"current,omitempty"`
	Previous        *quotaPeriod `json:"previous,omitempty"`
	EstimateExpired bool         `json:"-"`
}

type quotaSnapshot struct {
	Version   int                    `json:"version"`
	StartedAt time.Time              `json:"started_at"`
	Facts     map[string]quotaFact   `json:"facts"`
	Windows   map[string]quotaWindow `json:"windows"`
	Deleted   map[string]time.Time   `json:"deleted,omitempty"`
}

type quotaState struct {
	quotaSnapshot
	VersionCounter uint64
	NextPrune      time.Time
}

type quotaCycleDTO struct {
	StartAt     time.Time          `json:"start_at"`
	EndAt       time.Time          `json:"end_at"`
	ObservedAt  time.Time          `json:"observed_at"`
	UsedPercent *float64           `json:"used_percent"`
	Summary     *quotaUsageSummary `json:"summary"`
	ModelStats  []quotaModelStat   `json:"model_stats"`
}

type quotaUsageSummary struct {
	APIDetailSummary
	CostUSD *float64 `json:"estimated_cost"`
}

type quotaModelStat struct {
	ModelStat
	CostUSD *float64 `json:"estimated_cost"`
}

type quotaCurrentDTO struct {
	quotaCycleDTO
	EstimatedTotalUSD     *float64 `json:"estimated_total_usd"`
	EstimatedRemainingUSD *float64 `json:"estimated_remaining_usd"`
}

type quotaGroupDTO struct {
	GroupID       string           `json:"group_id"`
	Name          string           `json:"name"`
	WindowSeconds int64            `json:"window_seconds"`
	Current       *quotaCurrentDTO `json:"current"`
	Previous      *quotaCycleDTO   `json:"previous"`
}

type quotaCredentialDTO struct {
	Provider       string          `json:"provider"`
	AuthIndex      string          `json:"auth_index"`
	CredentialName string          `json:"credential_name"`
	Groups         []quotaGroupDTO `json:"groups"`
}

type quotaSignalsInput struct {
	Provider   string            `json:"provider"`
	AuthIndex  string            `json:"auth_index"`
	AuthID     string            `json:"auth_id"`
	ObservedAt time.Time         `json:"observed_at"`
	Signals    map[string]string `json:"signals"`
}

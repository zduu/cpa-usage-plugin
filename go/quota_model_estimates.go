package main

import "math"

// Convert the displayed period budget using each model's average cost per
// token for the same period. A missing budget or zero/unknown price cannot
// establish a finite model-only token capacity.
func applyQuotaModelEstimates(dto *quotaCycleDTO) {
	if dto == nil || dto.Unmapped {
		return
	}
	total := dto.EstimatedTotalUSD
	if dto.UsedPercent != nil && *dto.UsedPercent == 100 {
		total = dto.ActualTotalUSD
	}
	for i := range dto.ModelStats {
		row := &dto.ModelStats[i]
		row.ModelOnlyTotalTokens = nil
		if total == nil || *total < 0 || math.IsNaN(*total) || math.IsInf(*total, 0) ||
			row.CostUSD == nil || *row.CostUSD <= 0 || math.IsNaN(*row.CostUSD) || math.IsInf(*row.CostUSD, 0) || row.TotalTokens <= 0 {
			continue
		}
		tokens := *total / *row.CostUSD * float64(row.TotalTokens)
		if !math.IsNaN(tokens) && !math.IsInf(tokens, 0) && tokens >= 0 {
			row.ModelOnlyTotalTokens = &tokens
		}
	}
}

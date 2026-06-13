package consensus

import (
	"encoding/json"
	"math"
)

// scoreAgreement computes a confidence-weighted agreement score across provider results.
// It compares JSON fields: for each top-level field, providers that returned the same
// value as the confidence-weighted majority contribute to agreement.
// Returns: agreementScore [0,1], divergenceFlags (fields with disagreement), consensusOutput.
func scoreAgreement(results []ProviderResult) (score float64, diverged []string, output json.RawMessage) {
	valid := make([]ProviderResult, 0, len(results))
	for _, r := range results {
		if !r.Cancelled && r.Output != nil {
			valid = append(valid, r)
		}
	}
	if len(valid) == 0 {
		return 0, nil, nil
	}

	// Parse each provider's output as a flat map.
	parsed := make([]map[string]any, len(valid))
	for i, r := range valid {
		var m map[string]any
		if err := json.Unmarshal(r.Output, &m); err != nil {
			return 0, nil, nil
		}
		parsed[i] = m
	}

	// Collect all field names.
	fieldSet := map[string]struct{}{}
	for _, m := range parsed {
		for k := range m {
			fieldSet[k] = struct{}{}
		}
	}
	if len(fieldSet) == 0 {
		return 1.0, nil, valid[0].Output
	}

	totalWeight := 0.0
	for _, r := range valid {
		totalWeight += math.Max(r.Confidence, 0.01)
	}

	fieldScores := make(map[string]float64, len(fieldSet))
	for field := range fieldSet {
		// Group providers by value string, accumulate confidence weights.
		weights := map[string]float64{}
		for i, r := range valid {
			val := marshalVal(parsed[i][field])
			weights[val] += math.Max(r.Confidence, 0.01)
		}
		// Find the plurality value.
		best := ""
		bestW := 0.0
		for v, w := range weights {
			if w > bestW {
				bestW = w
				best = v
			}
		}
		_ = best
		fieldScores[field] = bestW / totalWeight
	}

	// Aggregate field scores into overall agreement score.
	sum := 0.0
	for _, s := range fieldScores {
		sum += s
	}
	score = sum / float64(len(fieldScores))

	// Fields below the consensus threshold are divergence flags.
	for field, s := range fieldScores {
		if s < ThresholdConsensus {
			diverged = append(diverged, field)
		}
	}

	// Build consensus output: majority value per field.
	consensusMap := map[string]any{}
	for field := range fieldSet {
		weights := map[string]float64{}
		valMap := map[string]any{}
		for i, r := range valid {
			val := marshalVal(parsed[i][field])
			weights[val] += math.Max(r.Confidence, 0.01)
			valMap[val] = parsed[i][field]
		}
		best := ""
		bestW := 0.0
		for v, w := range weights {
			if w > bestW {
				bestW = w
				best = v
			}
		}
		consensusMap[field] = valMap[best]
	}
	b, err := json.Marshal(consensusMap)
	if err != nil {
		return score, diverged, valid[0].Output
	}
	return score, diverged, b
}

func marshalVal(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

package reflection

import (
	"fmt"
	"strings"
)

func ensureFloatMap(values map[string]float64) map[string]float64 {
	if values == nil {
		return make(map[string]float64)
	}
	return values
}

func defaultDimensionRange() ValueRange { return ValueRange{Min: -1, Max: 1} }

func defaultRelationshipRange(name string) ValueRange {
	switch strings.ToLower(name) {
	case "trust", "attachment", "respect", "irritation", "irritability", "jealousy",
		"resentment", "gratitude", "closeness", "reliability", "warmth":
		return ValueRange{Min: 0, Max: 1}
	default:
		return defaultDimensionRange()
	}
}

func defaultTraitRange(name string) ValueRange {
	// Most configurable persona intensities are naturally represented in
	// [0,1]. Unknown adapter-defined traits use the signed generic range.
	switch strings.ToLower(name) {
	case "warmth", "trust", "attachment", "jealousy", "irritability", "romantic_tone",
		"romanticity", "emotionality", "directness", "playfulness", "formality",
		"reliability", "closeness", "respect", "empathy", "sociability", "shyness",
		"anxiety", "fearfulness", "emotional_stability", "sensitivity", "possessiveness",
		"initiative", "impulsivity", "stubbornness", "optimism", "curiosity", "suspicion",
		"tsundere":
		return ValueRange{Min: 0, Max: 1}
	default:
		return defaultDimensionRange()
	}
}

func lookupRange(ranges map[string]ValueRange, name string, fallback ValueRange) ValueRange {
	if value, ok := ranges[strings.ToLower(name)]; ok {
		return value
	}
	if fallback == (ValueRange{}) {
		return defaultTraitRange(name)
	}
	return fallback
}

func validateRanges(ranges map[string]ValueRange, label string) error {
	for name, value := range ranges {
		if err := validateName(name); err != nil || !value.Valid() {
			return fmt.Errorf("%w: invalid %s entry %q", ErrInvalidSnapshot, label, name)
		}
	}
	return nil
}

func cloneRanges(input map[string]ValueRange) map[string]ValueRange {
	if input == nil {
		return nil
	}
	output := make(map[string]ValueRange, len(input))
	for name, value := range input {
		output[strings.ToLower(name)] = value
	}
	return output
}

func cloneLowerFloatMap(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}
	output := make(map[string]float64, len(input))
	for name, value := range input {
		output[strings.ToLower(name)] = value
	}
	return output
}

func cloneBoolMap(input map[string]bool) map[string]bool {
	if input == nil {
		return nil
	}
	output := make(map[string]bool, len(input))
	for name, value := range input {
		output[strings.ToLower(name)] = value
	}
	return output
}

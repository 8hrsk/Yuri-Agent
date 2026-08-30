package domain

import (
	"math"
)

func validDimensionName(name string) bool {
	return validSimpleName(name)
}

func validEmotionName(name string) bool {
	return validSimpleName(name)
}

func validSimpleName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for index, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '_' && index > 0) {
			continue
		}
		return false
	}
	return name[0] != '_' && name[len(name)-1] != '_'
}

func cloneFloatMap(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}
	result := make(map[string]float64, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func floatMapsEqual(left, right map[string]float64) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if other, ok := right[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

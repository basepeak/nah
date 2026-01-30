package apply

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// getNestedValue retrieves a value from a nested map using path segments.
func getNestedValue(data map[string]any, segments []string) (any, bool) {
	if len(segments) == 0 {
		return nil, false
	}
	current := any(data)
	for _, seg := range segments {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// setNestedValue sets a value in a nested map using path segments.
// Returns true if the value was set.
func setNestedValue(data map[string]any, segments []string, value any) bool {
	if len(segments) == 0 {
		return false
	}
	current := data
	for _, seg := range segments[:len(segments)-1] {
		next, ok := current[seg]
		if !ok {
			return false
		}
		m, ok := next.(map[string]any)
		if !ok {
			return false
		}
		current = m
	}
	current[segments[len(segments)-1]] = value
	return true
}

// hashSubtree computes a deterministic SHA-256 hash of a JSON-serializable value,
// returning a prefixed hex string.
func hashSubtree(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("hash subtree: %w", err)
	}
	sum := sha256.Sum256(b)
	return hashPrefix + hex.EncodeToString(sum[:]), nil
}

// applyNoPruneRules restores the original (un-pruned) values at NoPrune paths
// in the pruned data map.
func applyNoPruneRules(pruned, original map[string]any, rules []pathRule) {
	for _, r := range rules {
		if r.strategy != ComparisonStrategyNoPrune {
			continue
		}
		origVal, ok := getNestedValue(original, r.segments)
		if !ok {
			continue
		}
		setNestedValue(pruned, r.segments, origVal)
	}
}

// applyHashRules replaces Hash-path subtrees with their hash markers in-place.
func applyHashRules(data map[string]any, rules []pathRule) error {
	for _, r := range rules {
		if r.strategy != ComparisonStrategyHash {
			continue
		}
		val, ok := getNestedValue(data, r.segments)
		if !ok {
			continue
		}
		h, err := hashSubtree(val)
		if err != nil {
			return err
		}
		setNestedValue(data, r.segments, h)
	}
	return nil
}

// applyHashRulesOnBytes applies hash rules to JSON bytes via round-trip.
func applyHashRulesOnBytes(b []byte, rules []pathRule) ([]byte, error) {
	if len(rules) == 0 {
		return b, nil
	}
	hasHashRules := false
	for _, r := range rules {
		if r.strategy == ComparisonStrategyHash {
			hasHashRules = true
			break
		}
	}
	if !hasHashRules {
		return b, nil
	}

	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	if err := applyHashRules(data, rules); err != nil {
		return nil, err
	}
	return json.Marshal(data)
}

// patchContainsHashedPaths checks if any hash-strategy path appears in the patch
// with a value that starts with the hash prefix.
func patchContainsHashedPaths(patch []byte, rules []pathRule) bool {
	if len(rules) == 0 {
		return false
	}
	var data map[string]any
	if err := json.Unmarshal(patch, &data); err != nil {
		return false
	}
	for _, r := range rules {
		if r.strategy != ComparisonStrategyHash {
			continue
		}
		val, ok := getNestedValue(data, r.segments)
		if !ok {
			continue
		}
		if s, ok := val.(string); ok && strings.HasPrefix(s, hashPrefix) {
			return true
		}
	}
	return false
}

// replaceHashedPathsWithCurrentValues takes the original (annotation) bytes and
// the current (live) object bytes, and replaces hash markers in original with
// the corresponding current live values. This allows a second three-way merge
// to produce a correct applicable patch with real values.
func replaceHashedPathsWithCurrentValues(original, current []byte, rules []pathRule) ([]byte, error) {
	var origData map[string]any
	if err := json.Unmarshal(original, &origData); err != nil {
		return nil, err
	}
	var curData map[string]any
	if err := json.Unmarshal(current, &curData); err != nil {
		return nil, err
	}

	for _, r := range rules {
		if r.strategy != ComparisonStrategyHash {
			continue
		}
		curVal, ok := getNestedValue(curData, r.segments)
		if !ok {
			continue
		}
		setNestedValue(origData, r.segments, curVal)
	}
	return json.Marshal(origData)
}

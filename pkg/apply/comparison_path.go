package apply

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// jsonNumberDecoder unmarshals JSON while preserving number precision by using
// json.Number instead of float64.
func jsonUnmarshalPreserveNumbers(b []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	return dec.Decode(v)
}

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
// returning a prefixed hex string. Determinism relies on Go's json.Marshal
// sorting map keys alphabetically.
func hashSubtree(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("hash subtree: %w", err)
	}
	sum := sha256.Sum256(b)
	return hashPrefix + hex.EncodeToString(sum[:]), nil
}

// applyNoPruneRules restores the original (un-pruned) values at NoPrune paths
// in the pruned data map. This is needed because pruneValues() is applied
// globally first (for backward compatibility), and NoPrune paths must then be
// selectively restored to preserve full fidelity for the three-way merge.
func applyNoPruneRules(pruned, original map[string]any, rules []pathRule) error {
	for _, r := range rules {
		if r.strategy != ComparisonStrategyNoPrune {
			continue
		}
		origVal, ok := getNestedValue(original, r.segments)
		if !ok {
			continue
		}
		if !setNestedValue(pruned, r.segments, origVal) {
			return fmt.Errorf("failed to restore NoPrune value at path %s", strings.Join(r.segments, "."))
		}
	}
	return nil
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
		if !setNestedValue(data, r.segments, h) {
			return fmt.Errorf("failed to set hash at path %s", strings.Join(r.segments, "."))
		}
	}
	return nil
}

// applyHashRulesOnBytes unmarshals JSON bytes, applies hash rules to the
// resulting map, and re-marshals back to JSON. Returns the input bytes
// unchanged if no Hash-strategy rules are present. Uses json.Number to
// preserve integer precision during the round-trip.
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
	if err := jsonUnmarshalPreserveNumbers(b, &data); err != nil {
		return nil, fmt.Errorf("applyHashRulesOnBytes: unmarshal: %w", err)
	}
	if err := applyHashRules(data, rules); err != nil {
		return nil, fmt.Errorf("applyHashRulesOnBytes: apply rules: %w", err)
	}
	return json.Marshal(data)
}

// patchContainsHashedPaths checks if any hash-strategy path appears in the patch
// with a value that starts with the hash prefix.
func patchContainsHashedPaths(patch []byte, rules []pathRule) (bool, error) {
	if len(rules) == 0 {
		return false, nil
	}
	var data map[string]any
	if err := json.Unmarshal(patch, &data); err != nil {
		return false, fmt.Errorf("patchContainsHashedPaths: unmarshal patch: %w", err)
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
			return true, nil
		}
	}
	return false, nil
}

// replaceHashedPathsWithCurrentValues takes the original (annotation) bytes and
// the current (live) object bytes, and replaces hash markers in original with
// the corresponding current live values. This allows a second three-way merge
// to produce a correct applicable patch with real values.
func replaceHashedPathsWithCurrentValues(original, current []byte, rules []pathRule) ([]byte, error) {
	var origData map[string]any
	if err := jsonUnmarshalPreserveNumbers(original, &origData); err != nil {
		return nil, fmt.Errorf("unmarshal original annotation: %w", err)
	}
	var curData map[string]any
	if err := jsonUnmarshalPreserveNumbers(current, &curData); err != nil {
		return nil, fmt.Errorf("unmarshal current object: %w", err)
	}

	for _, r := range rules {
		if r.strategy != ComparisonStrategyHash {
			continue
		}
		curVal, ok := getNestedValue(curData, r.segments)
		if !ok {
			continue
		}
		if !setNestedValue(origData, r.segments, curVal) {
			return nil, fmt.Errorf("failed to replace hashed path %s with current value", strings.Join(r.segments, "."))
		}
	}
	return json.Marshal(origData)
}

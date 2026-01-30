package apply

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetNestedValue(t *testing.T) {
	data := map[string]any{
		"spec": map[string]any{
			"values": map[string]any{
				"key": "val",
			},
			"simple": "hello",
		},
	}

	val, ok := getNestedValue(data, []string{"spec", "values"})
	assert.True(t, ok)
	assert.Equal(t, map[string]any{"key": "val"}, val)

	val, ok = getNestedValue(data, []string{"spec", "simple"})
	assert.True(t, ok)
	assert.Equal(t, "hello", val)

	_, ok = getNestedValue(data, []string{"spec", "missing"})
	assert.False(t, ok)

	_, ok = getNestedValue(data, []string{"missing"})
	assert.False(t, ok)

	_, ok = getNestedValue(data, nil)
	assert.False(t, ok)
}

func TestSetNestedValue(t *testing.T) {
	data := map[string]any{
		"spec": map[string]any{
			"values": "old",
		},
	}

	ok := setNestedValue(data, []string{"spec", "values"}, "new")
	assert.True(t, ok)
	assert.Equal(t, "new", data["spec"].(map[string]any)["values"])

	ok = setNestedValue(data, []string{"spec", "missing", "deep"}, "x")
	assert.False(t, ok)

	ok = setNestedValue(data, nil, "x")
	assert.False(t, ok)
}

func TestHashSubtree(t *testing.T) {
	val := map[string]any{"key": "value"}
	h1, err := hashSubtree(val)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(h1, hashPrefix))

	// Deterministic: same input produces same hash.
	h2, err := hashSubtree(val)
	require.NoError(t, err)
	assert.Equal(t, h1, h2)

	// Different input produces different hash.
	h3, err := hashSubtree(map[string]any{"key": "other"})
	require.NoError(t, err)
	assert.NotEqual(t, h1, h3)
}

func TestApplyNoPruneRules(t *testing.T) {
	original := map[string]any{
		"spec": map[string]any{
			"values": strings.Repeat("x", 200),
			"other":  "short",
		},
	}
	pruned := map[string]any{
		"spec": map[string]any{
			"values": strings.Repeat("x", 64) + "abcd1234", // pruned
			"other":  "short",
		},
	}

	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyNoPrune},
	}

	applyNoPruneRules(pruned, original, rules)
	assert.Equal(t, strings.Repeat("x", 200), pruned["spec"].(map[string]any)["values"])
	assert.Equal(t, "short", pruned["spec"].(map[string]any)["other"])
}

func TestApplyHashRules(t *testing.T) {
	data := map[string]any{
		"spec": map[string]any{
			"values": map[string]any{"key": "val"},
		},
	}

	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyHash},
	}

	err := applyHashRules(data, rules)
	require.NoError(t, err)

	v := data["spec"].(map[string]any)["values"]
	s, ok := v.(string)
	assert.True(t, ok)
	assert.True(t, strings.HasPrefix(s, hashPrefix))
}

func TestApplyHashRulesOnBytes(t *testing.T) {
	data := map[string]any{
		"spec": map[string]any{
			"values": map[string]any{"key": "val"},
		},
	}
	b, _ := json.Marshal(data)

	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyHash},
	}

	result, err := applyHashRulesOnBytes(b, rules)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(result, &out))

	v := out["spec"].(map[string]any)["values"]
	assert.True(t, strings.HasPrefix(v.(string), hashPrefix))

	// No rules -> bytes unchanged.
	result2, err := applyHashRulesOnBytes(b, nil)
	require.NoError(t, err)
	assert.Equal(t, b, result2)
}

func TestPatchContainsHashedPaths(t *testing.T) {
	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyHash},
	}

	patch := []byte(`{"spec":{"values":"nah-hash:sha256:abc123"}}`)
	assert.True(t, patchContainsHashedPaths(patch, rules))

	patch = []byte(`{"spec":{"values":"real-value"}}`)
	assert.False(t, patchContainsHashedPaths(patch, rules))

	patch = []byte(`{"spec":{"other":"nah-hash:sha256:abc123"}}`)
	assert.False(t, patchContainsHashedPaths(patch, rules))

	assert.False(t, patchContainsHashedPaths([]byte(`{}`), nil))
}

func TestReplaceHashedPathsWithCurrentValues(t *testing.T) {
	h, _ := hashSubtree(map[string]any{"key": "old"})
	original := map[string]any{
		"spec": map[string]any{
			"values": h,
		},
	}
	current := map[string]any{
		"spec": map[string]any{
			"values": map[string]any{"key": "old"},
			"other":  "keep",
		},
	}

	origBytes, _ := json.Marshal(original)
	curBytes, _ := json.Marshal(current)

	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyHash},
	}

	result, err := replaceHashedPathsWithCurrentValues(origBytes, curBytes, rules)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(result, &out))

	assert.Equal(t, map[string]any{"key": "old"}, out["spec"].(map[string]any)["values"])
}

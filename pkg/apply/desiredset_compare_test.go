package apply

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func makeUnstructured(data map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: data}
}

func TestSerializeApplied_NilRules_BackwardCompat(t *testing.T) {
	// With nil rules, serializeApplied should behave exactly as before:
	// strings > 64 chars get truncated with a hash suffix.
	longStr := strings.Repeat("a", 200)
	obj := makeUnstructured(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"data": map[string]any{
			"key": longStr,
		},
	})

	b, err := serializeApplied(obj, nil)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(b, &result))

	val := result["data"].(map[string]any)["key"].(string)
	// Should be truncated: 64 chars + 8 hex chars = 72
	assert.Equal(t, 72, len(val))
	assert.True(t, strings.HasPrefix(val, strings.Repeat("a", 64)))
}

func TestSerializeApplied_NoPrune(t *testing.T) {
	longStr := strings.Repeat("b", 200)
	obj := makeUnstructured(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"values": longStr,
		},
	})

	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyNoPrune},
	}

	b, err := serializeApplied(obj, rules)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(b, &result))

	// NoPrune should restore the full value.
	val := result["spec"].(map[string]any)["values"].(string)
	assert.Equal(t, longStr, val)
}

func TestSerializeApplied_Hash(t *testing.T) {
	obj := makeUnstructured(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"values": map[string]any{
				"nested": "data",
			},
		},
	})

	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyHash},
	}

	b, err := serializeApplied(obj, rules)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(b, &result))

	val := result["spec"].(map[string]any)["values"].(string)
	assert.True(t, strings.HasPrefix(val, hashPrefix))
}

func TestPrepareObjectForCreate_WithRules(t *testing.T) {
	longStr := strings.Repeat("c", 200)
	obj := makeUnstructured(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"values": longStr,
		},
	})

	gvk := obj.GroupVersionKind()
	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyNoPrune},
	}

	prepared, err := prepareObjectForCreate(gvk, obj, true, rules)
	require.NoError(t, err)

	ann := prepared.GetAnnotations()
	require.NotEmpty(t, ann[LabelApplied])

	// The applied annotation should contain the full long string.
	applied := appliedFromAnnotation(ann[LabelApplied])
	var data map[string]any
	require.NoError(t, json.Unmarshal(applied, &data))
	assert.Equal(t, longStr, data["spec"].(map[string]any)["values"])
}

func TestPrepareObjectForCreate_NilRules(t *testing.T) {
	obj := makeUnstructured(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"data": map[string]any{
			"key": "short",
		},
	})

	gvk := obj.GroupVersionKind()
	prepared, err := prepareObjectForCreate(gvk, obj, true, nil)
	require.NoError(t, err)
	assert.NotNil(t, prepared)
}

func TestOriginalAndModified_NoPruneNoSpuriousDiff(t *testing.T) {
	longStr := strings.Repeat("d", 200)
	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyNoPrune},
	}

	// Simulate first reconcile: create the object with applied annotation.
	obj := makeUnstructured(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"values": longStr,
		},
	})

	gvk := obj.GroupVersionKind()
	prepared, err := prepareObjectForCreate(gvk, obj, true, rules)
	require.NoError(t, err)

	// Now simulate second reconcile: same desired object.
	desired := makeUnstructured(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"values": longStr,
		},
	})

	original, modified, err := originalAndModified(gvk, prepared, desired, rules)
	require.NoError(t, err)

	// The three-way merge should produce no diff.
	current, err := json.Marshal(prepared)
	require.NoError(t, err)

	_, patch, err := createPatch(gvk, original, modified, current)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(patch))
}

func TestOriginalAndModified_HashNoSpuriousDiff(t *testing.T) {
	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyHash},
	}

	valuesMap := map[string]any{
		"large": strings.Repeat("e", 200),
	}

	// Use an unregistered GVK so createPatch uses JSON merge patch
	// instead of strategic merge (which would reject "spec" on ConfigMap).
	obj := makeUnstructured(map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"values": valuesMap,
		},
	})

	gvk := obj.GroupVersionKind()
	prepared, err := prepareObjectForCreate(gvk, obj, true, rules)
	require.NoError(t, err)

	// Same desired object.
	desired := makeUnstructured(map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"values": map[string]any{
				"large": strings.Repeat("e", 200),
			},
		},
	})

	original, modified, err := originalAndModified(gvk, prepared, desired, rules)
	require.NoError(t, err)

	current, err := json.Marshal(prepared)
	require.NoError(t, err)

	// Apply hash rules to modified and current for comparison.
	modifiedHashed, err := applyHashRulesOnBytes(modified, rules)
	require.NoError(t, err)
	currentHashed, err := applyHashRulesOnBytes(current, rules)
	require.NoError(t, err)

	_, patch, err := createPatch(gvk, original, modifiedHashed, currentHashed)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(patch))
}

func TestOriginalAndModified_HashDetectsRealChange(t *testing.T) {
	rules := []pathRule{
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyHash},
	}

	// Use an unregistered GVK for JSON merge patch.
	obj := makeUnstructured(map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"values": map[string]any{
				"key": "old-value",
			},
		},
	})

	gvk := obj.GroupVersionKind()
	prepared, err := prepareObjectForCreate(gvk, obj, true, rules)
	require.NoError(t, err)

	// Desired object has DIFFERENT values.
	desired := makeUnstructured(map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"values": map[string]any{
				"key": "new-value",
			},
		},
	})

	original, modified, err := originalAndModified(gvk, prepared, desired, rules)
	require.NoError(t, err)

	current, err := json.Marshal(prepared)
	require.NoError(t, err)

	// Phase 1: hashed comparison should detect the change.
	modifiedHashed, err := applyHashRulesOnBytes(modified, rules)
	require.NoError(t, err)
	currentHashed, err := applyHashRulesOnBytes(current, rules)
	require.NoError(t, err)

	_, patch, err := createPatch(gvk, original, modifiedHashed, currentHashed)
	require.NoError(t, err)
	assert.NotEqual(t, "{}", string(patch), "Phase 1 should detect hash difference")

	// The patch should contain the hash prefix (triggering Phase 2).
	found, err := patchContainsHashedPaths(patch, rules)
	require.NoError(t, err)
	assert.True(t, found)

	// Phase 2: regenerate with real values.
	originalForApply, err := replaceHashedPathsWithCurrentValues(original, current, rules)
	require.NoError(t, err)

	_, phase2Patch, err := createPatch(gvk, originalForApply, modified, current)
	require.NoError(t, err)
	assert.NotEqual(t, "{}", string(phase2Patch), "Phase 2 should produce a real patch")

	// Phase 2 patch should contain the actual new value, not hash markers.
	assert.Contains(t, string(phase2Patch), "new-value")
	assert.NotContains(t, string(phase2Patch), hashPrefix)
}

func TestWithComparisonStrategy_CopyOnWrite(t *testing.T) {
	base := apply{
		defaultNamespace: "default",
	}

	stratA := ComparisonStrategy{Path: "spec.a", Strategy: ComparisonStrategyNoPrune}
	stratB := ComparisonStrategy{Path: "spec.b", Strategy: ComparisonStrategyHash}

	a1 := base.WithComparisonStrategy(stratA).(apply)
	a2 := base.WithComparisonStrategy(stratB).(apply)

	// base should have no comparison config.
	assert.Nil(t, base.comparison)

	// a1 should only have stratA.
	gvk := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Foo"}
	rulesA := a1.comparison.rulesFor(gvk)
	require.Len(t, rulesA, 1)
	assert.Equal(t, []string{"spec", "a"}, rulesA[0].segments)

	// a2 should only have stratB, not stratA.
	rulesB := a2.comparison.rulesFor(gvk)
	require.Len(t, rulesB, 1)
	assert.Equal(t, []string{"spec", "b"}, rulesB[0].segments)
}

func TestSerializeApplied_CombinedStrategies(t *testing.T) {
	longStr := strings.Repeat("f", 200)
	obj := makeUnstructured(map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"config": longStr,
			"values": map[string]any{
				"nested": "data",
			},
		},
	})

	rules := []pathRule{
		{segments: []string{"spec", "config"}, strategy: ComparisonStrategyNoPrune},
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyHash},
	}

	b, err := serializeApplied(obj, rules)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(b, &result))

	spec := result["spec"].(map[string]any)

	// NoPrune path should have the full string.
	assert.Equal(t, longStr, spec["config"])

	// Hash path should have a hash marker.
	valStr, ok := spec["values"].(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(valStr, hashPrefix))
}

func TestCombinedStrategies_NoSpuriousDiff(t *testing.T) {
	longStr := strings.Repeat("g", 200)
	rules := []pathRule{
		{segments: []string{"spec", "config"}, strategy: ComparisonStrategyNoPrune},
		{segments: []string{"spec", "values"}, strategy: ComparisonStrategyHash},
	}

	obj := makeUnstructured(map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"config": longStr,
			"values": map[string]any{
				"nested": strings.Repeat("h", 200),
			},
		},
	})

	gvk := obj.GroupVersionKind()
	prepared, err := prepareObjectForCreate(gvk, obj, true, rules)
	require.NoError(t, err)

	// Same desired object.
	desired := makeUnstructured(map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "default",
		},
		"spec": map[string]any{
			"config": longStr,
			"values": map[string]any{
				"nested": strings.Repeat("h", 200),
			},
		},
	})

	original, modified, err := originalAndModified(gvk, prepared, desired, rules)
	require.NoError(t, err)

	current, err := json.Marshal(prepared)
	require.NoError(t, err)

	modifiedHashed, err := applyHashRulesOnBytes(modified, rules)
	require.NoError(t, err)
	currentHashed, err := applyHashRulesOnBytes(current, rules)
	require.NoError(t, err)

	_, patch, err := createPatch(gvk, original, modifiedHashed, currentHashed)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(patch))
}

func TestComparisonConfig_RulesFor(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Foo"}
	otherGVK := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Bar"}

	cfg := &comparisonConfig{
		global: []pathRule{
			{segments: []string{"spec", "values"}, strategy: ComparisonStrategyNoPrune},
		},
		byGVK: map[schema.GroupVersionKind][]pathRule{
			gvk: {
				{segments: []string{"spec", "extra"}, strategy: ComparisonStrategyHash},
			},
		},
	}

	// Nil config returns nil.
	var nilCfg *comparisonConfig
	assert.Nil(t, nilCfg.rulesFor(gvk))

	// Global rules always present; GVK-specific rules merged.
	rules := cfg.rulesFor(gvk)
	assert.Len(t, rules, 2)

	// Other GVK only gets global rules.
	rules = cfg.rulesFor(otherGVK)
	assert.Len(t, rules, 1)
}

package apply

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ComparisonStrategyType defines how a field path is handled during the
// three-way merge comparison in Ensure/Apply.
type ComparisonStrategyType int

const (
	// ComparisonStrategyNoPrune stores the full value at the path in the
	// "last applied" annotation, bypassing the default 64-char truncation.
	ComparisonStrategyNoPrune ComparisonStrategyType = iota

	// ComparisonStrategyHash replaces the subtree at the path with its
	// SHA-256 hash for storage and comparison. When a real change is
	// detected the patch is regenerated with full values.
	ComparisonStrategyHash
)

const hashPrefix = "nah-hash:sha256:"

// ComparisonStrategy associates a dot-separated JSON path with a strategy.
type ComparisonStrategy struct {
	Path     string
	Strategy ComparisonStrategyType
}

// pathRule is the internal, parsed form of ComparisonStrategy.
type pathRule struct {
	segments []string
	strategy ComparisonStrategyType
}

// comparisonConfig holds global and per-GVK path rules.
type comparisonConfig struct {
	global []pathRule
	byGVK  map[schema.GroupVersionKind][]pathRule
}

// rulesFor returns the merged set of global and GVK-specific rules.
func (c *comparisonConfig) rulesFor(gvk schema.GroupVersionKind) []pathRule {
	if c == nil {
		return nil
	}
	rules := make([]pathRule, 0, len(c.global)+len(c.byGVK[gvk]))
	rules = append(rules, c.global...)
	rules = append(rules, c.byGVK[gvk]...)
	return rules
}

func parseStrategies(strategies []ComparisonStrategy) []pathRule {
	rules := make([]pathRule, 0, len(strategies))
	for _, s := range strategies {
		rules = append(rules, pathRule{
			segments: strings.Split(s.Path, "."),
			strategy: s.Strategy,
		})
	}
	return rules
}

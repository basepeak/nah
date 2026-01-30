package apply

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ComparisonStrategyType defines how a field path is handled during the
// three-way merge comparison in Ensure/Apply.
type ComparisonStrategyType int

const (
	// ComparisonStrategyNoPrune stores the full value at the path in the
	// "last applied" annotation, bypassing the default pruning that replaces
	// strings longer than 64 characters with a 64-char prefix plus an 8-char
	// SHA-256 digest.
	//
	// Note: this stores the entire value in the annotation. For very large
	// values, be mindful of the Kubernetes annotation size limit (256 KB total
	// per object).
	ComparisonStrategyNoPrune ComparisonStrategyType = iota

	// ComparisonStrategyHash replaces the subtree at the path with its
	// SHA-256 hash for both annotation storage and initial comparison
	// (Phase 1). If the resulting patch contains hash markers rather than
	// real values, a second comparison (Phase 2) is performed with hash
	// markers replaced by live cluster values, producing an applicable
	// patch containing real data.
	ComparisonStrategyHash
)

// String returns the name of the strategy.
func (s ComparisonStrategyType) String() string {
	switch s {
	case ComparisonStrategyNoPrune:
		return "NoPrune"
	case ComparisonStrategyHash:
		return "Hash"
	default:
		return fmt.Sprintf("ComparisonStrategyType(%d)", int(s))
	}
}

const hashPrefix = "nah-hash:sha256:"

// ComparisonStrategy associates a dot-separated JSON path with a strategy.
// The path is split on "." to form segments for nested map traversal
// (e.g., "spec.values.config"). Field names containing literal dots are
// not supported.
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

func parseStrategies(strategies []ComparisonStrategy) ([]pathRule, error) {
	rules := make([]pathRule, 0, len(strategies))
	for _, s := range strategies {
		if s.Path == "" {
			return nil, fmt.Errorf("comparison strategy path must not be empty")
		}
		segments := strings.Split(s.Path, ".")
		for _, seg := range segments {
			if seg == "" {
				return nil, fmt.Errorf("comparison strategy path %q contains empty segment", s.Path)
			}
		}
		rules = append(rules, pathRule{
			segments: segments,
			strategy: s.Strategy,
		})
	}
	return rules, nil
}

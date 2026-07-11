package server

import "testing"

func TestPublicSnapshotRuleSortsUsePriorityThenID(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		rules := []publicRateLimitRuleConfig{
			{ID: 30, Priority: 20},
			{ID: 20, Priority: 10},
			{ID: 10, Priority: 10},
		}
		sortPublicRateLimitRules(rules)
		assertRuleIDs(t, []int64{rules[0].ID, rules[1].ID, rules[2].ID}, []int64{10, 20, 30})
	})

	t.Run("traffic shaper", func(t *testing.T) {
		rules := []publicTrafficShaperRuleConfig{
			{ID: 30, Priority: 20},
			{ID: 20, Priority: 10},
			{ID: 10, Priority: 10},
		}
		sortPublicTrafficShaperRules(rules)
		assertRuleIDs(t, []int64{rules[0].ID, rules[1].ID, rules[2].ID}, []int64{10, 20, 30})
	})

	t.Run("WAF", func(t *testing.T) {
		rules := []publicWafRuleConfig{
			{ID: 30, Priority: 20},
			{ID: 20, Priority: 10},
			{ID: 10, Priority: 10},
		}
		sortPublicWafRules(rules)
		assertRuleIDs(t, []int64{rules[0].ID, rules[1].ID, rules[2].ID}, []int64{10, 20, 30})
	})

	t.Run("cache", func(t *testing.T) {
		rules := []publicCacheRuleConfig{
			{ID: 30, Priority: 20},
			{ID: 20, Priority: 10},
			{ID: 10, Priority: 10},
		}
		sortPublicCacheRules(rules)
		assertRuleIDs(t, []int64{rules[0].ID, rules[1].ID, rules[2].ID}, []int64{10, 20, 30})
	})
}

func assertRuleIDs(t testing.TB, got []int64, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rule IDs len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("rule IDs = %v, want %v", got, want)
		}
	}
}

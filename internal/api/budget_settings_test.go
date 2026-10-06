package api

import (
	"context"
	"slices"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestBudgetNamespacesAreNormalized(t *testing.T) {
	cfg := &finopsv1.CostDeckConfig{}
	budgets := []finopsv1.Budget{{
		Name: "dev", Scope: "namespace", Value: "dev-api", MonthlyLimit: "300",
		Namespaces: []string{"dev-web", "dev-api", "dev-db", "dev-web"},
	}}
	if err := applyBudgetSettings(context.Background(), cfg, &SettingsUpdateRequest{Budgets: &budgets}); err != nil {
		t.Fatal(err)
	}
	got := cfg.Spec.Budgets[0]
	if got.Value != "" || !slices.Equal(got.Namespaces, []string{"dev-api", "dev-db", "dev-web"}) {
		t.Errorf("namespaces should be one sorted list without duplicates, value folded in: %+v", got)
	}

	for name, b := range map[string]finopsv1.Budget{
		"no namespace":    {Name: "x", Scope: "namespace", MonthlyLimit: "1"},
		"bad namespace":   {Name: "x", Scope: "namespace", Namespaces: []string{"Not_A_Namespace"}, MonthlyLimit: "1"},
		"list on a team":  {Name: "x", Scope: "team", Value: "payments", Namespaces: []string{"a"}, MonthlyLimit: "1"},
		"list on cluster": {Name: "x", Scope: "cluster", Namespaces: []string{"a"}, MonthlyLimit: "1"},
	} {
		list := []finopsv1.Budget{b}
		if err := applyBudgetSettings(context.Background(), &finopsv1.CostDeckConfig{}, &SettingsUpdateRequest{Budgets: &list}); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
}

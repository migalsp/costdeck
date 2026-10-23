package finops

import (
	"math"
	"strings"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// One day: 100 of nodes, 10 of volumes and 5 of load balancers. Pods request 70, so 30 of
// capacity is unrequested; kube-system and monitoring (10 each) are shared.
const allocationCurrency = "EUR"

func allocationLedger() *Ledger {
	day := func(date string) Day {
		return Day{Date: date, Hours: 24, Provisioned: 100, Requested: 70, Storage: 10, Network: 5, Namespaces: map[string]NamespaceDay{
			"shop-dev":    {Cost: 30, Storage: 6, Network: 5},
			"shop-prod":   {Cost: 20, Storage: 4},
			"kube-system": {Cost: 10},
			"monitoring":  {Cost: 10},
		}}
	}
	return &Ledger{Currency: allocationCurrency, Days: []Day{day("2026-09-30"), day("2026-10-01")}}
}

var allocationLabels = map[string]map[string]string{
	"shop-dev":  {"team": "payments", "cost-center": "cc-1"},
	"shop-prod": {"team": "payments", "env": "prod"},
}

func rowsOf(r AllocationReport) map[string]AllocationRow {
	out := map[string]AllocationRow{}
	for _, row := range r.Rows {
		out[row.Key] = row
	}
	return out
}

func TestAllocate(t *testing.T) {
	shared := []string{"kube-*", "monitor*"}
	cases := []struct {
		name        string
		policy      finopsv1.AllocationConfig
		by          string
		want        map[string]float64
		unallocated float64
	}{
		{"nothing shared, idle apart", finopsv1.AllocationConfig{}, AllocateByNamespace,
			map[string]float64{"shop-dev": 41, "shop-prod": 24, "kube-system": 10, "monitoring": 10}, 30},
		{"shared by requests", finopsv1.AllocationConfig{SharedNamespaces: shared}, AllocateByNamespace,
			map[string]float64{"shop-dev": 53, "shop-prod": 32}, 30},
		{"shared evenly", finopsv1.AllocationConfig{SharedNamespaces: shared, SharedSplit: SplitEven}, AllocateByNamespace,
			map[string]float64{"shop-dev": 51, "shop-prod": 34}, 30},
		{"idle charged too", finopsv1.AllocationConfig{SharedNamespaces: shared, Idle: IdleProportional}, AllocateByNamespace,
			map[string]float64{"shop-dev": 71, "shop-prod": 44}, 0},
		{"by team", finopsv1.AllocationConfig{SharedNamespaces: shared}, AllocateByTeam,
			map[string]float64{"payments": 85}, 30},
		{"by any label", finopsv1.AllocationConfig{SharedNamespaces: shared}, AllocateByLabel + "cost-center",
			map[string]float64{"cc-1": 53, "(no cost-center)": 32}, 30},
		{"everything shared, nobody to charge", finopsv1.AllocationConfig{SharedNamespaces: []string{"*"}, Idle: IdleProportional}, AllocateByNamespace,
			map[string]float64{"shop-dev": 41, "shop-prod": 24, "kube-system": 10, "monitoring": 10}, 30},
	}
	for _, c := range cases {
		r := Allocate(allocationLedger(), allocationLabels, c.policy, c.by, "2026-10-01", "2026-10-31")
		got := rowsOf(r)
		if len(got) != len(c.want) {
			t.Errorf("%s: rows %+v", c.name, r.Rows)
			continue
		}
		for key, total := range c.want {
			if math.Abs(got[key].Total-total) > 0.005 {
				t.Errorf("%s: %s = %v, want %v (%+v)", c.name, key, got[key].Total, total, got[key])
			}
		}
		if r.Unallocated != c.unallocated {
			t.Errorf("%s: unallocated %v, want %v", c.name, r.Unallocated, c.unallocated)
		}
		// The groups add up to the bill: nothing is lost or counted twice.
		sum := r.Unallocated
		for _, row := range r.Rows {
			sum += row.Total
		}
		if r.Days != 1 || r.Total != 115 || math.Abs(sum-r.Total) > 0.01 {
			t.Errorf("%s: %d days, total %v, rows add up to %v", c.name, r.Days, r.Total, sum)
		}
	}
}

func TestAllocationDetails(t *testing.T) {
	r := Allocate(allocationLedger(), allocationLabels, finopsv1.AllocationConfig{SharedNamespaces: []string{"kube-*", "monitor*"}}, AllocateByTeam, "2026-09-01", "2026-10-31")
	if r.Days != 2 || r.Total != 230 || r.Currency != allocationCurrency {
		t.Errorf("two days: %+v", r)
	}
	row := r.Rows[0]
	if strings.Join(row.Namespaces, ",") != "shop-dev,shop-prod" || row.Compute != 100 || row.Storage != 20 || row.Network != 10 || row.Shared != 40 {
		t.Errorf("payments = %+v", row)
	}
	if strings.Join(r.SharedNamespaces, ",") != "kube-system,monitoring" {
		t.Errorf("shared = %v", r.SharedNamespaces)
	}
	// A namespace deleted since has no labels left.
	l := allocationLedger()
	l.Days[1].Namespaces["pr-41"] = NamespaceDay{Cost: 5}
	if got := rowsOf(Allocate(l, allocationLabels, finopsv1.AllocationConfig{}, AllocateByTeam, "2026-10-01", "2026-10-01")); got["(no team)"].Total == 0 {
		t.Errorf("a deleted namespace is not charged to (no team): %+v", got)
	}
	if AllocationKey(AllocateByEnvironment, "shop-prod", allocationLabels["shop-prod"]) != EnvProduction {
		t.Error("the environment comes from the env label, as everywhere else")
	}
	if !IsShared("kube-public", []string{"kube-*"}) || IsShared("shop", []string{"[bad"}) {
		t.Error("pattern matching")
	}
}

package finops

import (
	"testing"
	"time"
)

func TestSummaryOf(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	o := Overview{
		GeneratedAt: at, Currency: "EUR",
		Cluster:     Cluster{Nodes: 4, ProvisionedHourly: 99},
		Namespaces:  []NamespaceRow{{Name: "shop"}, {Name: "dev"}, {Name: "ops"}},
		Monthly:     Monthly{Provisioned: 900, Total: 1000, Requested: 600, Used: 300, Unallocated: 300},
		Savings:     Savings{SchedulesMonthly: 120, RightsizingMonthly: 50, ScheduleCandidatesMonthly: 25, RightsizingPending: 2},
		MonthToDate: MonthToDate{Cost: 700, Forecast: 1010},
	}
	want := ClusterSummary{
		Name: "prod-eu", GeneratedAt: at, Currency: "EUR", Nodes: 4, Namespaces: 3,
		MonthlyCost: 1000, MonthlyRequested: 600, MonthlyUsed: 300, MonthlyUnallocated: 300,
		SchedulesSaving: 120, SavingsAvailable: 75, MonthToDate: 700, Forecast: 1010,
	}
	if got := SummaryOf("prod-eu", o); got != want {
		t.Errorf("SummaryOf =\n%+v\nwant\n%+v\n(the whole bill as cost, right-sizing plus schedule candidates as available savings, no schedule counts)", got, want)
	}
}

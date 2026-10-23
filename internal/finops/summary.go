package finops

import "time"

// ClusterSummary is a cluster's cost at a glance: what a Cost Deck that shows several
// clusters reads from each one (GET /api/finops/summary).
type ClusterSummary struct {
	Name        string    `json:"name"`
	GeneratedAt time.Time `json:"generatedAt"`
	Currency    string    `json:"currency"`
	Nodes       int       `json:"nodes"`
	Namespaces  int       `json:"namespaces"`
	// MonthlyCost is the whole bill at the current run rate: nodes, volumes and load
	// balancers. MonthlyRequested and MonthlyUsed are the node cost pods request and use.
	MonthlyCost        float64 `json:"monthlyCost"`
	MonthlyRequested   float64 `json:"monthlyRequested"`
	MonthlyUsed        float64 `json:"monthlyUsed"`
	MonthlyUnallocated float64 `json:"monthlyUnallocated"`
	// SchedulesSaving is what the schedules keep down now, a month at this rate;
	// SavingsAvailable what right-sizing and schedules on unscheduled namespaces would add.
	SchedulesSaving  float64 `json:"schedulesSaving"`
	SavingsAvailable float64 `json:"savingsAvailable"`
	MonthToDate      float64 `json:"monthToDate"`
	Forecast         float64 `json:"forecast"`
	// Schedules counts ScalingGroups and ScalingConfigs, SchedulesDown those scaled down.
	Schedules     int `json:"schedules"`
	SchedulesDown int `json:"schedulesDown"`
}

// SummaryOf condenses an overview. It also reads the overview of a Cost Deck too old to
// serve a summary, which leaves the schedule counts at zero.
func SummaryOf(name string, o Overview) ClusterSummary {
	return ClusterSummary{
		Name: name, GeneratedAt: o.GeneratedAt, Currency: o.Currency, Nodes: o.Cluster.Nodes, Namespaces: len(o.Namespaces),
		MonthlyCost: o.Monthly.Total, MonthlyRequested: o.Monthly.Requested, MonthlyUsed: o.Monthly.Used, MonthlyUnallocated: o.Monthly.Unallocated,
		SchedulesSaving: o.Savings.SchedulesMonthly, SavingsAvailable: o.Savings.RightsizingMonthly + o.Savings.ScheduleCandidatesMonthly,
		MonthToDate: o.MonthToDate.Cost, Forecast: o.MonthToDate.Forecast,
	}
}

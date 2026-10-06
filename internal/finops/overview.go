package finops

import (
	"fmt"
	"sort"
	"time"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

const (
	// workingHoursUpShare is the share of the week a "Working hours" schedule (08:00–19:00,
	// Monday to Friday) keeps a namespace up: 55 of 168 hours.
	workingHoursUpShare = 55.0 / 168.0
	// idleCapacityThreshold is the share of the node bill left unrequested that is worth
	// pointing out.
	idleCapacityThreshold = 0.25
	// costIncreaseThreshold flags namespaces whose last seven days cost this much more than
	// the seven before.
	costIncreaseThreshold = 0.20
	historyDays           = 30
	trendDays             = 14
	topNamespaces         = 5
	// OtherNamespaces collects the namespaces outside the top ones in History.
	OtherNamespaces  = "__other__"
	maxOpportunities = 12
)

// Overview is everything the cost overview and the namespace table show.
type Overview struct {
	GeneratedAt   time.Time      `json:"generatedAt"`
	Currency      string         `json:"currency"`
	Rates         pricing.Rates  `json:"rates"`
	Cluster       Cluster        `json:"cluster"`
	Monthly       Monthly        `json:"monthly"`
	Savings       Savings        `json:"savings"`
	MonthToDate   MonthToDate    `json:"monthToDate"`
	History       History        `json:"history"`
	Namespaces    []NamespaceRow `json:"namespaces"`
	Opportunities []Opportunity  `json:"opportunities"`
	// InfraError says why volumes and load balancers are missing, if they are.
	InfraError string `json:"infraError,omitempty"`
}

// Monthly is the cluster's cost at the current run rate, split the FinOps way: what the
// nodes cost, how much of it pods request, and how much they use.
type Monthly struct {
	Provisioned float64 `json:"provisioned"`
	Requested   float64 `json:"requested"`
	Used        float64 `json:"used"`
	// Unallocated is node capacity no pod requests (provisioned − requested).
	Unallocated float64 `json:"unallocated"`
	// Overprovisioned is requested but unused (requested − used, per namespace).
	Overprovisioned float64 `json:"overprovisioned"`
	// Storage and Network are persistent volumes and load balancers; Total is the whole
	// bill: nodes, storage and network.
	Storage float64 `json:"storage"`
	Network float64 `json:"network"`
	Total   float64 `json:"total"`
	// UnusedStorage and IdleNetwork are the parts of them nothing uses.
	UnusedStorage float64 `json:"unusedStorage"`
	IdleNetwork   float64 `json:"idleNetwork"`
}

// Savings sums what is saved and what could be.
type Savings struct {
	// SchedulesMonthly is what the schedules keep down right now, as a monthly rate.
	SchedulesMonthly float64 `json:"schedulesMonthly"`
	// RightsizingMonthly sums the right-sizing advice of every namespace analysed so far.
	RightsizingMonthly float64 `json:"rightsizingMonthly"`
	// RightsizingPending counts namespaces whose advice is still being calculated.
	RightsizingPending int `json:"rightsizingPending"`
	// ScheduleCandidatesMonthly is what working-hours schedules on unscheduled
	// non-production namespaces would save.
	ScheduleCandidatesMonthly float64 `json:"scheduleCandidatesMonthly"`
}

// MonthToDate is the current UTC month according to the ledger.
type MonthToDate struct {
	Month    string  `json:"month"` // YYYY-MM
	Cost     float64 `json:"cost"`
	Saved    float64 `json:"saved"`
	Hours    float64 `json:"hours"`
	Forecast float64 `json:"forecast"`
	// Partial is true when the ledger does not cover the whole month so far.
	Partial bool `json:"partial"`
}

// History is the daily cost of the last days, with the biggest namespaces broken out.
type History struct {
	Since time.Time    `json:"since,omitzero"`
	Top   []string     `json:"top"`
	Days  []HistoryDay `json:"days"`
}

// HistoryDay is one day of History.
type HistoryDay struct {
	Date        string             `json:"date"`
	Hours       float64            `json:"hours"`
	Provisioned float64            `json:"provisioned"`
	Requested   float64            `json:"requested"`
	Used        float64            `json:"used"`
	Saved       float64            `json:"saved"`
	Storage     float64            `json:"storage"`
	Network     float64            `json:"network"`
	Namespaces  map[string]float64 `json:"namespaces"`
}

// NamespaceRow is one namespace in the overview.
type NamespaceRow struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Environment string            `json:"environment"`
	Team        string            `json:"team,omitempty"`
	Pods        int               `json:"pods"`
	Collected   bool              `json:"collected"`
	CPU         Resource          `json:"cpu"`
	Memory      Resource          `json:"memoryGiB"`
	MonthlyCost float64           `json:"monthlyCost"`
	MonthlyUsed float64           `json:"monthlyUsedCost"`
	MonthlyIdle float64           `json:"monthlyIdle"`
	// MonthlyStorage and MonthlyNetwork are the namespace's volumes and load balancers;
	// MonthlyTotal adds them to its compute (MonthlyCost).
	MonthlyStorage float64 `json:"monthlyStorage"`
	MonthlyNetwork float64 `json:"monthlyNetwork"`
	MonthlyTotal   float64 `json:"monthlyTotal"`
	// Efficiency is used cost over requested cost; absent without requests.
	Efficiency *float64     `json:"efficiency,omitempty"`
	Insights   []string     `json:"insights"`
	Schedule   *ScheduleRef `json:"schedule,omitempty"`
	// RightsizingMonthly is absent while the advice is still being calculated.
	RightsizingMonthly *float64 `json:"rightsizingMonthly,omitempty"`
	// ScheduleSavingMonthly is set for unscheduled non-production namespaces.
	ScheduleSavingMonthly float64 `json:"scheduleSavingMonthly,omitempty"`
	// Cost7d and CostPrev7d compare the last seven days with the seven before, once the
	// ledger covers them.
	Cost7d     *float64  `json:"cost7d,omitempty"`
	CostPrev7d *float64  `json:"costPrev7d,omitempty"`
	Trend      []float64 `json:"trend,omitempty"`
}

// Resource is requested, used and limit in cores or GiB.
type Resource struct {
	Requested float64 `json:"requested"`
	Used      float64 `json:"used"`
	Limit     float64 `json:"limit"`
}

// ScheduleRef names the schedule that governs a namespace.
type ScheduleRef struct {
	Kind         string  `json:"kind"`
	Name         string  `json:"name"`
	DesiredState string  `json:"desiredState,omitempty"`
	Mode         string  `json:"mode,omitempty"`
	SavedMonthly float64 `json:"savedMonthly"`
}

// Opportunity is one thing worth doing, ranked by what it saves.
type Opportunity struct {
	Kind           string  `json:"kind"` // rightsizing, schedule, idle-capacity, cost-increase, missing-requests
	Namespace      string  `json:"namespace,omitempty"`
	Title          string  `json:"title"`
	Detail         string  `json:"detail"`
	MonthlySavings float64 `json:"monthlySavings,omitempty"`
}

// Opportunity kinds.
const (
	OpportunityUnusedVolumes   = "unused-volumes"
	OpportunityOrphanVolumes   = "orphaned-volumes"
	OpportunityIdleLB          = "idle-load-balancers"
	OpportunityRightsizing     = "rightsizing"
	OpportunitySchedule        = "schedule"
	OpportunityIdleCapacity    = "idle-capacity"
	OpportunityCostIncrease    = "cost-increase"
	OpportunityMissingRequests = "missing-requests"
)

// NewOverview combines a snapshot, the ledger and the right-sizing advice known so far
// (namespace → monthly savings; namespaces missing from it are counted as pending).
func NewOverview(s Snapshot, ledger *Ledger, rightsizing map[string]float64, pending int) Overview {
	m := func(hourly float64) float64 { return hourly * pricing.HoursPerMonth }
	o := Overview{
		GeneratedAt: s.At,
		Currency:    s.Rates.Currency,
		Rates:       s.Rates,
		Cluster:     s.Cluster,
		Monthly: Monthly{
			Provisioned:   m(s.Cluster.ProvisionedHourly),
			Requested:     m(s.Cluster.RequestedHourly),
			Used:          m(s.Cluster.UsedHourly),
			Unallocated:   max(0, m(s.Cluster.ProvisionedHourly-s.Cluster.RequestedHourly)),
			Storage:       m(s.Cluster.StorageHourly),
			Network:       m(s.Cluster.NetworkHourly),
			Total:         m(s.Cluster.BillHourly()),
			UnusedStorage: s.Infra.UnusedStorageMonthly,
			IdleNetwork:   s.Infra.IdleNetworkMonthly,
		},
		InfraError:    s.InfraError,
		Savings:       Savings{SchedulesMonthly: m(s.SavedHourly()), RightsizingPending: pending},
		Namespaces:    []NamespaceRow{},
		Opportunities: []Opportunity{},
	}
	if ledger == nil {
		ledger = &Ledger{}
	}
	o.History = history(ledger)
	o.MonthToDate = monthToDate(ledger, s)
	week, prevWeek, covered := weeks(ledger, s.At)

	for _, ns := range s.Namespaces {
		row := NamespaceRow{
			Name: ns.Name, Labels: ns.Labels, Pods: ns.Pods, Collected: ns.Collected,
			Environment: Environment(ns.Name, ns.Labels), Team: Team(ns.Labels),
			CPU:         Resource{Requested: ns.CPURequested, Used: ns.CPUUsed, Limit: ns.CPULimit},
			Memory:      Resource{Requested: ns.MemRequested, Used: ns.MemUsed, Limit: ns.MemLimit},
			MonthlyCost: m(ns.RequestedHourly), MonthlyUsed: m(ns.UsedHourly), MonthlyIdle: m(ns.IdleHourly),
			MonthlyStorage: m(ns.StorageHourly), MonthlyNetwork: m(ns.NetworkHourly), MonthlyTotal: m(ns.TotalHourly()),
			Insights: ns.Insights,
		}
		if row.Insights == nil {
			row.Insights = []string{}
		}
		if ns.RequestedHourly > 0 {
			e := ns.UsedHourly / ns.RequestedHourly
			row.Efficiency = &e
		}
		o.Monthly.Overprovisioned += row.MonthlyIdle
		if sc, ok := s.ScheduleFor(ns.Name); ok {
			row.Schedule = &ScheduleRef{Kind: sc.Kind, Name: sc.Name, DesiredState: sc.DesiredState, Mode: sc.Mode, SavedMonthly: m(sc.SavedHourly)}
		} else if row.Environment == EnvNonProduction && row.MonthlyCost > 0 {
			row.ScheduleSavingMonthly = row.MonthlyCost * (1 - workingHoursUpShare)
			o.Savings.ScheduleCandidatesMonthly += row.ScheduleSavingMonthly
		}
		if v, ok := rightsizing[ns.Name]; ok {
			row.RightsizingMonthly = &v
			o.Savings.RightsizingMonthly += v
		}
		if covered >= 1 {
			w := week[ns.Name]
			row.Cost7d = &w
		}
		if covered >= 2 {
			p := prevWeek[ns.Name]
			row.CostPrev7d = &p
		}
		row.Trend = trend(ledger, ns.Name, s.At)
		o.Namespaces = append(o.Namespaces, row)
	}
	o.Opportunities = opportunities(o, s.Infra)
	return o
}

func history(l *Ledger) History {
	h := History{Since: l.Since, Top: []string{}, Days: []HistoryDay{}}
	days := l.Days
	if len(days) > historyDays {
		days = days[len(days)-historyDays:]
	}
	totals := map[string]float64{}
	for _, d := range days {
		for ns, v := range d.Namespaces {
			totals[ns] += v.Total()
		}
	}
	names := make([]string, 0, len(totals))
	for ns := range totals {
		names = append(names, ns)
	}
	sort.Slice(names, func(i, j int) bool {
		if totals[names[i]] != totals[names[j]] {
			return totals[names[i]] > totals[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > topNamespaces {
		names = names[:topNamespaces]
	}
	h.Top = names
	top := map[string]bool{}
	for _, n := range names {
		top[n] = true
	}
	for _, d := range days {
		hd := HistoryDay{Date: d.Date, Hours: d.Hours, Provisioned: d.Provisioned, Requested: d.Requested, Used: d.Used, Saved: d.Saved,
			Storage: d.Storage, Network: d.Network, Namespaces: map[string]float64{}}
		for ns, v := range d.Namespaces {
			if top[ns] {
				hd.Namespaces[ns] += v.Total()
			} else {
				hd.Namespaces[OtherNamespaces] += v.Total()
			}
		}
		h.Days = append(h.Days, hd)
	}
	return h
}

// monthToDate adds up the current month and projects it at the current run rate.
func monthToDate(l *Ledger, s Snapshot) MonthToDate {
	now := s.At.UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	mtd := MonthToDate{Month: start.Format("2006-01")}
	prefix := mtd.Month + "-"
	for _, d := range l.Days {
		if len(d.Date) >= len(prefix) && d.Date[:len(prefix)] == prefix {
			mtd.Cost += d.Bill()
			mtd.Saved += d.Saved
			mtd.Hours += d.Hours
		}
	}
	elapsed := now.Sub(start).Hours()
	// Allow for the sampling interval before calling the month incomplete.
	mtd.Partial = mtd.Hours < elapsed-1
	mtd.Forecast = mtd.Cost + s.Cluster.BillHourly()*end.Sub(now).Hours()
	return mtd
}

// weeks sums each namespace's cost over the last seven days and the seven before. covered
// says how many of the two weeks the ledger spans.
func weeks(l *Ledger, now time.Time) (week, prev map[string]float64, covered int) {
	week, prev = map[string]float64{}, map[string]float64{}
	if len(l.Days) == 0 {
		return week, prev, 0
	}
	weekStart := DateOf(now.AddDate(0, 0, -6))
	prevStart := DateOf(now.AddDate(0, 0, -13))
	for _, d := range l.Days {
		switch {
		case d.Date >= weekStart:
			for ns, v := range d.Namespaces {
				week[ns] += v.Total()
			}
		case d.Date >= prevStart:
			for ns, v := range d.Namespaces {
				prev[ns] += v.Total()
			}
		}
	}
	covered = 1
	// The previous week only counts when the ledger started before it did.
	if DateOf(l.Since) <= prevStart {
		covered = 2
	}
	return week, prev, covered
}

func trend(l *Ledger, ns string, now time.Time) []float64 {
	if len(l.Days) < 2 {
		return nil
	}
	from := DateOf(now.AddDate(0, 0, -(trendDays - 1)))
	var out []float64
	for _, d := range l.Days {
		if d.Date >= from {
			out = append(out, round(d.Namespaces[ns].Total(), 4))
		}
	}
	return out
}

func opportunities(o Overview, inf Infra) []Opportunity {
	ops := []Opportunity{}
	cur := o.Currency
	for _, ns := range o.Namespaces {
		if ns.RightsizingMonthly != nil && *ns.RightsizingMonthly >= 0.01 {
			ops = append(ops, Opportunity{
				Kind: OpportunityRightsizing, Namespace: ns.Name, MonthlySavings: *ns.RightsizingMonthly,
				Title:  fmt.Sprintf("Right-size %s", ns.Name),
				Detail: "Some containers request much more than they use. The namespace page has the values and YAML.",
			})
		}
		if ns.ScheduleSavingMonthly >= 0.01 {
			ops = append(ops, Opportunity{
				Kind: OpportunitySchedule, Namespace: ns.Name, MonthlySavings: ns.ScheduleSavingMonthly,
				Title:  fmt.Sprintf("Put %s on a schedule", ns.Name),
				Detail: "It looks like a non-production namespace and runs around the clock. Working hours (08:00–19:00, Monday to Friday) would keep it down two thirds of the week.",
			})
		}
		if ns.Cost7d != nil && ns.CostPrev7d != nil && *ns.CostPrev7d > 0 {
			change := *ns.Cost7d / *ns.CostPrev7d - 1
			if change >= costIncreaseThreshold && *ns.Cost7d-*ns.CostPrev7d >= 0.01 {
				ops = append(ops, Opportunity{
					Kind: OpportunityCostIncrease, Namespace: ns.Name,
					Title:  fmt.Sprintf("%s costs %.0f%% more than the week before", ns.Name, change*100),
					Detail: fmt.Sprintf("%s in the last 7 days against %s in the 7 before. Check what was scaled up or added.", money(*ns.Cost7d, cur), money(*ns.CostPrev7d, cur)),
				})
			}
		}
	}
	ops = append(ops, infraOpportunities(inf)...)
	if p := o.Monthly.Provisioned; p > 0 && o.Monthly.Unallocated/p >= idleCapacityThreshold {
		ops = append(ops, Opportunity{
			Kind: OpportunityIdleCapacity, MonthlySavings: o.Monthly.Unallocated,
			Title: fmt.Sprintf("%.0f%% of node capacity is not requested by any pod", o.Monthly.Unallocated/p*100),
			Detail: "You pay for it whether pods use it or not. Fewer or smaller nodes, or an autoscaler such as Cluster Autoscaler or Karpenter, " +
				"would recover part of it. Part of every node is reserved for the system and can never be requested.",
		})
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].MonthlySavings > ops[j].MonthlySavings })
	var missing []string
	for _, ns := range o.Namespaces {
		for _, in := range ns.Insights {
			if in == "Missing Requests" && ns.Environment != EnvSystem {
				missing = append(missing, ns.Name)
			}
		}
	}
	for _, name := range missing {
		ops = append(ops, Opportunity{
			Kind: OpportunityMissingRequests, Namespace: name,
			Title:  fmt.Sprintf("Set requests in %s", name),
			Detail: "Containers without CPU or memory requests are invisible to the scheduler and to cost allocation, so this namespace looks cheaper than it is.",
		})
	}
	if len(ops) > maxOpportunities {
		ops = ops[:maxOpportunities]
	}
	return ops
}

// infraOpportunities points at volumes and load balancers nothing uses.
func infraOpportunities(inf Infra) []Opportunity {
	var out []Opportunity
	group := func(state string, vols bool) (names []string, total float64) {
		if vols {
			for _, v := range inf.Volumes {
				if v.State == state && v.MonthlyCost > 0 {
					name := v.Volume
					if v.Claim != "" {
						name = v.Namespace + "/" + v.Claim
					}
					names = append(names, name)
					total += v.MonthlyCost
				}
			}
			return names, total
		}
		for _, lb := range inf.LoadBalancers {
			if lb.State == state && lb.MonthlyCost > 0 {
				names = append(names, lb.Namespace+"/"+lb.Name)
				total += lb.MonthlyCost
			}
		}
		return names, total
	}
	if names, total := group(StateUnused, true); len(names) > 0 {
		out = append(out, Opportunity{
			Kind: OpportunityUnusedVolumes, MonthlySavings: total,
			Title:  fmt.Sprintf("%d volume%s mounted by no pod", len(names), plural(len(names))),
			Detail: fmt.Sprintf("%s are bound but no running pod uses them. Delete them if the data is no longer needed, after a snapshot if in doubt.", list(names, 3)),
		})
	}
	if names, total := group(StateOrphaned, true); len(names) > 0 {
		out = append(out, Opportunity{
			Kind: OpportunityOrphanVolumes, MonthlySavings: total,
			Title:  fmt.Sprintf("%d released volume%s still billed", len(names), plural(len(names))),
			Detail: fmt.Sprintf("%s lost their claim, but the reclaim policy keeps the disks. Delete the PersistentVolumes and their cloud disks once the data is safe.", list(names, 3)),
		})
	}
	if names, total := group(StateIdle, false); len(names) > 0 {
		out = append(out, Opportunity{
			Kind: OpportunityIdleLB, MonthlySavings: total,
			Title:  fmt.Sprintf("%d load balancer%s with nothing behind %s", len(names), plural(len(names)), map[bool]string{true: "it", false: "them"}[len(names) == 1]),
			Detail: fmt.Sprintf("%s have no ready endpoint and still cost their hourly base price. Delete them, or route through a shared Ingress.", list(names, 3)),
		})
	}
	if names, total := group(StateScaledDown, false); len(names) > 0 {
		out = append(out, Opportunity{
			Kind: OpportunityIdleLB, MonthlySavings: total * (1 - workingHoursUpShare),
			Title:  fmt.Sprintf("%d load balancer%s billed while %s schedule keeps it down", len(names), plural(len(names)), map[bool]string{true: "its", false: "their"}[len(names) == 1]),
			Detail: fmt.Sprintf("%s keep costing money while their namespaces are scaled to zero. A shared Ingress controller in an always-on namespace avoids one load balancer per environment.", list(names, 3)),
		})
	}
	return out
}

func money(v float64, currency string) string {
	if currency == "" || currency == "USD" {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("%.2f %s", v, currency)
}

package finops

import (
	"path"
	"sort"
	"strings"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// Cost allocation charges every cost of a period to a group (a team, an environment, a
// namespace or the value of any namespace label), from the daily history. Shared
// namespaces are charged to the others, and capacity no pod requests is either a line of
// its own or charged to the namespaces too, so that the groups add up to the bill.

// Groups an allocation can be by.
const (
	AllocateByNamespace   = "namespace"
	AllocateByTeam        = "team"
	AllocateByEnvironment = "environment"
	// AllocateByLabel prefixes a label key: "label:cost-center".
	AllocateByLabel = "label:"
)

// Allocation policies, as stored in spec.allocation.
const (
	IdleSeparate      = "separate"
	IdleProportional  = "proportional"
	SplitProportional = "proportional"
	SplitEven         = "even"
)

// AllocationRow is what one group is charged over the period.
type AllocationRow struct {
	Key        string   `json:"key"`
	Namespaces []string `json:"namespaces"`
	Compute    float64  `json:"compute"`
	Storage    float64  `json:"storage"`
	Network    float64  `json:"network"`
	// Shared is its part of the shared namespaces, Idle its part of the capacity no pod
	// requested (with the proportional policy).
	Shared float64 `json:"shared"`
	Idle   float64 `json:"idle"`
	Total  float64 `json:"total"`
}

// AllocationReport charges a period's cost to groups.
type AllocationReport struct {
	Currency string                    `json:"currency"`
	From     string                    `json:"from"`
	To       string                    `json:"to"`
	By       string                    `json:"by"`
	Policy   finopsv1.AllocationConfig `json:"policy"`
	// Days is how many days of the period the history has.
	Days int             `json:"days"`
	Rows []AllocationRow `json:"rows"`
	// SharedNamespaces are the namespaces the policy shared out over the period.
	SharedNamespaces []string `json:"sharedNamespaces"`
	// Unallocated is the capacity no pod requested, when it is a line of its own.
	Unallocated float64 `json:"unallocated"`
	// Total is the bill for the days covered: the rows and Unallocated add up to it.
	Total float64 `json:"total"`
}

// IsShared reports whether a namespace matches one of the shared names or patterns.
func IsShared(ns string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, ns); err == nil && ok {
			return true
		}
	}
	return false
}

// AllocationKey is the group a namespace is charged to.
func AllocationKey(by, ns string, labels map[string]string) string {
	switch {
	case by == AllocateByNamespace:
		return ns
	case by == AllocateByEnvironment:
		return Environment(ns, labels)
	case strings.HasPrefix(by, AllocateByLabel):
		key := strings.TrimPrefix(by, AllocateByLabel)
		if v := labels[key]; v != "" {
			return v
		}
		return "(no " + key + ")"
	}
	if t := Team(labels); t != "" {
		return t
	}
	return "(no team)"
}

// share is one namespace's cost over the period.
type share struct{ compute, storage, network, shared, idle float64 }

// Allocate charges the days from..to (YYYY-MM-DD, inclusive) of the history to groups.
// labels are the namespaces' current labels; a deleted namespace has none.
func Allocate(l *Ledger, labels map[string]map[string]string, p finopsv1.AllocationConfig, by, from, to string) AllocationReport {
	r := AllocationReport{Currency: l.Currency, From: from, To: to, By: by, Policy: p, Rows: []AllocationRow{}, SharedNamespaces: []string{}}
	per := map[string]*share{}
	at := func(ns string) *share {
		if per[ns] == nil {
			per[ns] = &share{}
		}
		return per[ns]
	}
	sharedSeen := map[string]bool{}
	for _, d := range l.Days {
		if d.Date < from || d.Date > to {
			continue
		}
		r.Days++
		r.Total += d.Bill()
		var pool, requested, tenantCompute float64
		var tenants []string
		for ns, nd := range d.Namespaces {
			requested += nd.Cost
			if IsShared(ns, p.SharedNamespaces) {
				sharedSeen[ns] = true
				pool += nd.Total()
				continue
			}
			s := at(ns)
			s.compute += nd.Cost
			s.storage += nd.Storage
			s.network += nd.Network
			tenants = append(tenants, ns)
			tenantCompute += nd.Cost
		}
		sort.Strings(tenants)
		// spread hands an amount to the tenants by the compute they request, or evenly.
		spread := func(amount float64, even bool, add func(*share, float64)) bool {
			if amount <= 0 || len(tenants) == 0 {
				return amount <= 0
			}
			for _, ns := range tenants {
				w := 1 / float64(len(tenants))
				if !even && tenantCompute > 0 {
					w = d.Namespaces[ns].Cost / tenantCompute
				}
				add(at(ns), amount*w)
			}
			return true
		}
		if !spread(pool, p.SharedSplit == SplitEven, func(s *share, v float64) { s.shared += v }) {
			// Nobody to charge: the shared namespaces keep their own cost.
			for ns, nd := range d.Namespaces {
				if IsShared(ns, p.SharedNamespaces) {
					s := at(ns)
					s.compute, s.storage, s.network = s.compute+nd.Cost, s.storage+nd.Storage, s.network+nd.Network
				}
			}
		}
		idle := max(0, d.Provisioned-requested)
		if p.Idle != IdleProportional || !spread(idle, false, func(s *share, v float64) { s.idle += v }) {
			r.Unallocated += idle
		}
	}

	rows := map[string]*AllocationRow{}
	for ns, s := range per {
		key := AllocationKey(by, ns, labels[ns])
		row := rows[key]
		if row == nil {
			row = &AllocationRow{Key: key}
			rows[key] = row
		}
		row.Namespaces = append(row.Namespaces, ns)
		row.Compute += s.compute
		row.Storage += s.storage
		row.Network += s.network
		row.Shared += s.shared
		row.Idle += s.idle
	}
	for _, row := range rows {
		sort.Strings(row.Namespaces)
		row.Total = row.Compute + row.Storage + row.Network + row.Shared + row.Idle
		for _, v := range []*float64{&row.Compute, &row.Storage, &row.Network, &row.Shared, &row.Idle, &row.Total} {
			*v = round(*v, 2)
		}
		r.Rows = append(r.Rows, *row)
	}
	sort.Slice(r.Rows, func(i, j int) bool {
		if r.Rows[i].Total != r.Rows[j].Total {
			return r.Rows[i].Total > r.Rows[j].Total
		}
		return r.Rows[i].Key < r.Rows[j].Key
	})
	for ns := range sharedSeen {
		r.SharedNamespaces = append(r.SharedNamespaces, ns)
	}
	sort.Strings(r.SharedNamespaces)
	r.Unallocated, r.Total = round(r.Unallocated, 2), round(r.Total, 2)
	return r
}

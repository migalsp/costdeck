package api

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
)

// Periods the allocation report offers besides explicit from and to dates.
const (
	periodThisMonth = "this-month"
	periodLastMonth = "last-month"
	periodLast7     = "last-7-days"
	periodLast30    = "last-30-days"
)

// allocationQuery reads ?by=, and ?period= or ?from=&to= (YYYY-MM-DD, UTC).
func allocationQuery(q url.Values, now time.Time) (by, from, to string, err error) {
	by = q.Get("by")
	switch {
	case by == "":
		by = finops.AllocateByTeam
	case by == finops.AllocateByTeam || by == finops.AllocateByEnvironment || by == finops.AllocateByNamespace:
	case strings.HasPrefix(by, finops.AllocateByLabel):
		if errs := validation.IsQualifiedName(strings.TrimPrefix(by, finops.AllocateByLabel)); len(errs) > 0 {
			return "", "", "", fmt.Errorf("by: %s", strings.Join(errs, "; "))
		}
	default:
		return "", "", "", fmt.Errorf("by is team, environment, namespace or label:<key>")
	}
	now = now.UTC()
	day := func(t time.Time) string { return t.Format(time.DateOnly) }
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	from, to = q.Get("from"), q.Get("to")
	if from != "" || to != "" {
		f, err1 := time.Parse(time.DateOnly, from)
		t, err2 := time.Parse(time.DateOnly, to)
		if err1 != nil || err2 != nil || t.Before(f) {
			return "", "", "", fmt.Errorf("from and to are dates (YYYY-MM-DD), from first")
		}
		return by, from, to, nil
	}
	switch q.Get("period") {
	case "", periodThisMonth:
		return by, day(first), day(now), nil
	case periodLastMonth:
		return by, day(first.AddDate(0, -1, 0)), day(first.AddDate(0, 0, -1)), nil
	case periodLast7:
		return by, day(now.AddDate(0, 0, -6)), day(now), nil
	case periodLast30:
		return by, day(now.AddDate(0, 0, -29)), day(now), nil
	}
	return "", "", "", fmt.Errorf("period is this-month, last-month, last-7-days or last-30-days")
}

// allocation builds the report for a request's query.
func (s *Server) allocation(ctx context.Context, q url.Values) (finops.AllocationReport, error) {
	by, from, to, err := allocationQuery(q, time.Now())
	if err != nil {
		return finops.AllocationReport{}, badRequest{err}
	}
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		return finops.AllocationReport{}, err
	}
	ledger, err := s.costLedger(ctx)
	if err != nil {
		return finops.AllocationReport{}, err
	}
	var namespaces corev1.NamespaceList
	if err := s.Client.List(ctx, &namespaces); err != nil {
		return finops.AllocationReport{}, err
	}
	labels := make(map[string]map[string]string, len(namespaces.Items))
	for _, ns := range namespaces.Items {
		labels[ns.Name] = ns.Labels
	}
	return finops.Allocate(ledger, labels, cfg.Spec.Allocation, by, from, to), nil
}

// handleAllocation serves the cost of a period charged to teams, environments,
// namespaces or the values of a label.
func (s *Server) handleAllocation(w http.ResponseWriter, r *http.Request) {
	report, err := s.allocation(r.Context(), r.URL.Query())
	if err != nil {
		writeAllocationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handleAllocationCSV serves the same report as CSV for finance: one row per group, the
// unallocated capacity and the total.
func (s *Server) handleAllocationCSV(w http.ResponseWriter, r *http.Request) {
	report, err := s.allocation(r.Context(), r.URL.Query())
	if err != nil {
		writeAllocationError(w, err)
		return
	}
	group := strings.ReplaceAll(report.By, ":", "-")
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cost-allocation-%s-%s-%s.csv"`, group, report.From, report.To))
	cw := csv.NewWriter(w)
	money := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
	cur := report.Currency
	_ = cw.Write([]string{strings.TrimPrefix(report.By, finops.AllocateByLabel), "namespaces", "compute_" + cur, "storage_" + cur, "load_balancers_" + cur, "shared_" + cur, "idle_" + cur, "total_" + cur})
	for _, row := range report.Rows {
		_ = cw.Write([]string{row.Key, strings.Join(row.Namespaces, " "), money(row.Compute), money(row.Storage), money(row.Network), money(row.Shared), money(row.Idle), money(row.Total)})
	}
	if report.Unallocated > 0 {
		_ = cw.Write([]string{"(unallocated capacity)", "", "", "", "", "", money(report.Unallocated), money(report.Unallocated)})
	}
	_ = cw.Write([]string{"(total)", "", "", "", "", "", "", money(report.Total)})
	cw.Flush()
}

func writeAllocationError(w http.ResponseWriter, err error) {
	if br, ok := err.(badRequest); ok {
		writeError(w, http.StatusBadRequest, br.Error())
		return
	}
	writeK8sError(w, err)
}

// AllocationSettings is spec.allocation as the settings API reads and writes it.
type AllocationSettings struct {
	Idle             string   `json:"idle"`
	SharedNamespaces []string `json:"sharedNamespaces"`
	SharedSplit      string   `json:"sharedSplit"`
}

// AllocationUpdateRequest changes spec.allocation; a field left out stays as it is.
type AllocationUpdateRequest struct {
	Idle             *string   `json:"idle,omitempty"`
	SharedNamespaces *[]string `json:"sharedNamespaces,omitempty"`
	SharedSplit      *string   `json:"sharedSplit,omitempty"`
}

func allocationSettings(a finopsv1.AllocationConfig) AllocationSettings {
	out := AllocationSettings{Idle: a.Idle, SharedNamespaces: a.SharedNamespaces, SharedSplit: a.SharedSplit}
	if out.Idle == "" {
		out.Idle = finops.IdleSeparate
	}
	if out.SharedSplit == "" {
		out.SharedSplit = finops.SplitProportional
	}
	if out.SharedNamespaces == nil {
		out.SharedNamespaces = []string{}
	}
	return out
}

func applyAllocationSettings(_ context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	u := req.Allocation
	if u == nil {
		return nil
	}
	a := &cfg.Spec.Allocation
	if u.Idle != nil {
		if *u.Idle != finops.IdleSeparate && *u.Idle != finops.IdleProportional {
			return badRequestf("allocation: idle is separate or proportional")
		}
		a.Idle = *u.Idle
	}
	if u.SharedSplit != nil {
		if *u.SharedSplit != finops.SplitProportional && *u.SharedSplit != finops.SplitEven {
			return badRequestf("allocation: the shared split is proportional or even")
		}
		a.SharedSplit = *u.SharedSplit
	}
	if u.SharedNamespaces != nil {
		var out []string
		for _, p := range *u.SharedNamespaces {
			if p = strings.TrimSpace(p); p == "" || slices.Contains(out, p) {
				continue
			}
			if _, err := path.Match(p, ""); err != nil {
				return badRequestf("allocation: %q is not a name or a pattern such as kube-*", p)
			}
			out = append(out, p)
		}
		if len(out) > 100 {
			return badRequestf("allocation: at most 100 shared namespaces or patterns")
		}
		a.SharedNamespaces = out
	}
	return nil
}

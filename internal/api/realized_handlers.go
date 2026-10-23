package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch

// States of a realized-savings check.
const (
	realizedNone     = "none"     // nothing recorded: the target is up, or was down before
	realizedSettling = "settling" // autoscalers still have time to remove the nodes
	realizedChecked  = "checked"
)

// realizedResponse is GET /api/scaling/{groups|configs}/{name}/realized: whether the nodes
// the workloads ran on went away after the scale-down, and what keeps the others.
type realizedResponse struct {
	State           string     `json:"state"`
	ScaledDownAt    *time.Time `json:"scaledDownAt,omitempty"`
	SettleMinutes   int        `json:"settleMinutes"`
	EstimatedHourly float64    `json:"estimatedHourly"`
	Currency        string     `json:"currency"`
	finops.NodeCheckSummary
}

func (s *Server) handleGroupRealized(w http.ResponseWriter, r *http.Request) {
	group := &finopsv1.ScalingGroup{}
	if err := s.Client.Get(r.Context(), s.scalingKey(r), group); err != nil {
		writeK8sError(w, err)
		return
	}
	s.writeRealized(w, r, &group.Status.ScheduleStatus)
}

func (s *Server) handleConfigRealized(w http.ResponseWriter, r *http.Request) {
	cfg := &finopsv1.ScalingConfig{}
	if err := s.Client.Get(r.Context(), s.scalingKey(r), cfg); err != nil {
		writeK8sError(w, err)
		return
	}
	s.writeRealized(w, r, &cfg.Status.ScheduleStatus)
}

func (s *Server) writeRealized(w http.ResponseWriter, r *http.Request, st *finopsv1.ScheduleStatus) {
	resp, err := s.realized(r.Context(), st, time.Now())
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// realized checks the nodes a schedule recorded when its scale-down began.
func (s *Server) realized(ctx context.Context, st *finopsv1.ScheduleStatus, now time.Time) (realizedResponse, error) {
	resp := realizedResponse{State: realizedNone, SettleMinutes: int(finops.SettleTime / time.Minute), Currency: st.Currency,
		NodeCheckSummary: finops.NodeCheckSummary{Nodes: []finops.NodeCheck{}}}
	resp.EstimatedHourly, _ = strconv.ParseFloat(st.EstimatedHourlySavings, 64)
	if resp.Currency == "" {
		resp.Currency = s.costRates(ctx).Currency
	}
	if st.ScaleDownAt == nil || st.DesiredState != scaling.StateDown {
		return resp, nil
	}
	var nodes corev1.NodeList
	if err := s.Client.List(ctx, &nodes); err != nil {
		return resp, err
	}
	var pods corev1.PodList
	if err := s.Client.List(ctx, &pods); err != nil {
		return resp, err
	}
	var pdbs policyv1.PodDisruptionBudgetList
	if err := s.Client.List(ctx, &pdbs); err != nil {
		return resp, err
	}
	recorded := make([]finops.RecordedNode, 0, len(st.ScaleDownNodes))
	for _, n := range st.ScaleDownNodes {
		cost, _ := strconv.ParseFloat(n.HourlyCost, 64)
		recorded = append(recorded, finops.RecordedNode{Name: n.Name, HourlyCost: cost})
	}
	at := st.ScaleDownAt.Time
	resp.ScaledDownAt = &at
	resp.NodeCheckSummary = finops.CheckNodes(recorded, nodes.Items, pods.Items, pdbs.Items, now)
	resp.State = realizedChecked
	if now.Sub(at) < finops.SettleTime {
		resp.State = realizedSettling
	}
	return resp, nil
}

// toolRealized is the realized-savings check for the assistant and MCP.
func (s *Server) toolRealized(ctx context.Context, target, name string) (string, error) {
	var st *finopsv1.ScheduleStatus
	switch target {
	case targetGroup:
		g := &finopsv1.ScalingGroup{}
		if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: config.OperatorNamespace()}, g); err != nil {
			return "", fmt.Errorf("ScalingGroup %s: %w", name, err)
		}
		st = &g.Status.ScheduleStatus
	case targetNamespace:
		c, err := s.configForNamespace(ctx, name)
		if err != nil {
			return "", err
		}
		st = &c.Status.ScheduleStatus
	default:
		return "", fmt.Errorf("target must be group or namespace")
	}
	resp, err := s.realized(ctx, st, time.Now())
	if err != nil {
		return "", err
	}
	return toJSON(resp)
}

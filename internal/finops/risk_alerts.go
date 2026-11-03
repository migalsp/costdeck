package finops

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	RiskAlertsConfigMap = "costdeck-risk-alerts"
	riskStateKey        = "state.json"
	AlertRisk           = "risk"
	digestRisks         = 10
)

// OpenRisk is a critical performance risk found by the last check. Since is when it was first
// seen and is kept across checks for as long as the risk stays open.
type OpenRisk struct {
	Namespace string    `json:"namespace"`
	Kind      string    `json:"kind"`
	Workload  string    `json:"workload"`
	Container string    `json:"container,omitempty"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	Fix       string    `json:"fix,omitempty"`
	Since     time.Time `json:"since"`
}

// Key identifies a risk across checks by namespace, workload kind and name, container and risk
// code; it keys both maps of RiskState.
func (r OpenRisk) Key() string {
	return strings.Join([]string{r.Namespace, r.Kind, r.Workload, r.Container, r.Code}, "/")
}

// Subject names where the risk is for alert and digest lines: "namespace / workload", followed by
// " / container" when the risk is about one container.
func (r OpenRisk) Subject() string {
	subject := r.Namespace + " / " + r.Workload
	if r.Container != "" {
		subject += " / " + r.Container
	}
	return subject
}

// RiskState is what the costdeck-risk-alerts ConfigMap holds: the risks open at CheckedAt, keyed by
// OpenRisk.Key, and when each key was last announced, which spaces out repeat alerts.
type RiskState struct {
	Open      map[string]OpenRisk  `json:"open"`
	Fired     map[string]time.Time `json:"fired"`
	CheckedAt time.Time            `json:"checkedAt"`
}

// Sorted returns the open risks ordered by namespace, workload, container and code, so alerts and
// digests list them in a stable order.
func (st RiskState) Sorted() []OpenRisk {
	out := make([]OpenRisk, 0, len(st.Open))
	for _, r := range st.Open {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b OpenRisk) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Workload, b.Workload), cmp.Compare(a.Container, b.Container), cmp.Compare(a.Code, b.Code))
	})
	return out
}

// LoadRiskState reads the state from the costdeck-risk-alerts ConfigMap in namespace. A missing
// ConfigMap gives an empty state, unreadable JSON is ignored, and the maps are never nil.
func LoadRiskState(ctx context.Context, c client.Reader, namespace string) (RiskState, error) {
	st := RiskState{Open: map[string]OpenRisk{}, Fired: map[string]time.Time{}}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: RiskAlertsConfigMap}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return st, nil
		}
		return st, err
	}
	_ = json.Unmarshal([]byte(cm.Data[riskStateKey]), &st)
	if st.Open == nil {
		st.Open = map[string]OpenRisk{}
	}
	if st.Fired == nil {
		st.Fired = map[string]time.Time{}
	}
	return st, nil
}

// SaveRiskState writes the state into the costdeck-risk-alerts ConfigMap, creating it when it is
// missing and retrying on update conflicts.
func SaveRiskState(ctx context.Context, c client.Client, namespace string, st RiskState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cm corev1.ConfigMap
		getErr := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: RiskAlertsConfigMap}, &cm)
		create := apierrors.IsNotFound(getErr)
		if getErr != nil && !create {
			return getErr
		}
		if create {
			cm = corev1.ConfigMap{
				Name: RiskAlertsConfigMap, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck"}}
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[riskStateKey] = string(raw)
		if create {
			return c.Create(ctx, &cm)
		}
		return c.Update(ctx, &cm)
	})
}

// AppendAlertEvents puts fresh events in front of the alert history in the costdeck-alerts
// ConfigMap, shared with budget and anomaly alerts, and keeps the newest maxAlertEvents. It creates
// the ConfigMap when missing and retries on conflicts; an empty list writes nothing.
func AppendAlertEvents(ctx context.Context, c client.Client, namespace string, fresh []AlertEvent) error {
	if len(fresh) == 0 {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cm corev1.ConfigMap
		getErr := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: AlertsConfigMap}, &cm)
		create := apierrors.IsNotFound(getErr)
		if getErr != nil && !create {
			return getErr
		}
		events := []AlertEvent{}
		_ = json.Unmarshal([]byte(cm.Data[alertEventsKey]), &events)
		events = append(slices.Clone(fresh), events...)
		events = events[:min(len(events), maxAlertEvents)]
		raw, _ := json.Marshal(events)
		if create {
			cm = corev1.ConfigMap{
				Name: AlertsConfigMap, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck"}}
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[alertEventsKey] = string(raw)
		if create {
			return c.Create(ctx, &cm)
		}
		return c.Update(ctx, &cm)
	})
}

// WithOpenRisks adds the open critical risks from the saved state to a digest. When the state
// cannot be read the digest is returned without them rather than failing.
func WithOpenRisks(ctx context.Context, c client.Reader, namespace string, d Digest) Digest {
	st, err := LoadRiskState(ctx, c, namespace)
	if err != nil {
		return d
	}
	d.CriticalRisks = st.Sorted()
	return d
}

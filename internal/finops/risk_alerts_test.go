package finops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDigestListsOpenRisks(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()
	st := RiskState{Open: map[string]OpenRisk{}, Fired: map[string]time.Time{}}
	for i := range 12 {
		r := OpenRisk{Namespace: "shop", Kind: "Deployment", Workload: fmt.Sprintf("svc-%02d", i), Container: "main", Code: "oom-killed", Message: "OOM-killed."}
		st.Open[r.Key()] = r
	}
	if err := SaveRiskState(ctx, c, "costdeck", st); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRiskState(ctx, c, "costdeck")
	if err != nil || len(loaded.Open) != 12 {
		t.Fatalf("loaded = %d risks, %v", len(loaded.Open), err)
	}
	d := WithOpenRisks(ctx, c, "costdeck", Digest{Currency: "USD"})
	md := d.Markdown()
	if !strings.Contains(md, "**Performance risks**\n- shop / svc-00 / main: OOM-killed.") || !strings.Contains(md, "- and 2 more on their namespace pages") || strings.Contains(md, "svc-10") {
		t.Errorf("digest = %s", md)
	}
	if empty := WithOpenRisks(ctx, c, "elsewhere", Digest{Currency: "USD"}); strings.Contains(empty.Markdown(), "Performance risks") {
		t.Error("a digest without open risks has no risks section")
	}
}

func TestAppendAlertEvents(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	risk := func(title string) AlertEvent { return AlertEvent{At: at, Kind: AlertRisk, Title: title} }

	empty := testClient()
	if err := AppendAlertEvents(ctx, empty, "costdeck", nil); err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := empty.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: AlertsConfigMap}, &cm); !apierrors.IsNotFound(err) {
		t.Errorf("nothing to append still wrote the feed: %v", err)
	}

	c := testClient()
	if err := AppendAlertEvents(ctx, c, "costdeck", []AlertEvent{risk("first")}); err != nil {
		t.Fatal(err)
	}
	_ = c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: AlertsConfigMap}, &cm)
	if cm.Labels["app.kubernetes.io/managed-by"] != "costdeck" {
		t.Errorf("labels = %v", cm.Labels)
	}
	const alerterState = `{"fired":{"budget/cluster/2026-10/80":"2026-10-20T00:00:00Z"}}`
	cm.Data[alertStateKey] = alerterState
	if err := c.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	fresh := []AlertEvent{risk("newest"), risk("second")}
	if err := AppendAlertEvents(ctx, c, "costdeck", fresh); err != nil {
		t.Fatal(err)
	}
	ev, _ := AlertEvents(ctx, c, "costdeck")
	if len(ev) != 3 || ev[0].Title != "newest" || ev[1].Title != "second" || ev[2].Title != "first" {
		t.Errorf("feed = %+v, want the new events first in their order", ev)
	}
	_ = c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: AlertsConfigMap}, &cm)
	if cm.Data[alertStateKey] != alerterState {
		t.Errorf("the budget alerter's state was changed: %q", cm.Data[alertStateKey])
	}

	many := make([]AlertEvent, maxAlertEvents+5)
	for i := range many {
		many[i] = risk(fmt.Sprintf("burst %d", i))
	}
	if err := AppendAlertEvents(ctx, c, "costdeck", many); err != nil {
		t.Fatal(err)
	}
	if ev, _ := AlertEvents(ctx, c, "costdeck"); len(ev) != maxAlertEvents || ev[0].Title != "burst 0" {
		t.Errorf("feed has %d events starting with %q, want %d starting with the burst", len(ev), ev[0].Title, maxAlertEvents)
	}
	if err := AppendAlertEvents(ctx, clientFailingGets(), "costdeck", fresh); !errors.Is(err, errAPIDown) {
		t.Errorf("AppendAlertEvents = %v, want the read error", err)
	}
}

func TestRiskStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := testClient(&corev1.ConfigMap{Name: RiskAlertsConfigMap, Namespace: "costdeck", Data: map[string]string{riskStateKey: "{}"}})
	st, err := LoadRiskState(ctx, c, "costdeck")
	if err != nil || st.Open == nil || st.Fired == nil {
		t.Fatalf("LoadRiskState = %+v, %v; want usable empty maps", st, err)
	}
	r := OpenRisk{Namespace: "shop", Kind: "Deployment", Workload: "api", Code: "oom-killed"}
	st.Open[r.Key()] = r
	st.Fired[r.Key()] = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	if err := SaveRiskState(ctx, c, "costdeck", st); err != nil {
		t.Fatal(err)
	}
	delete(st.Open, r.Key())
	if err := SaveRiskState(ctx, c, "costdeck", st); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRiskState(ctx, c, "costdeck")
	if err != nil || len(loaded.Open) != 0 || len(loaded.Fired) != 1 {
		t.Errorf("state after the risk closed = %+v, %v; want no open risk and the fired key kept", loaded, err)
	}

	if _, err := LoadRiskState(ctx, clientFailingGets(), "costdeck"); !errors.Is(err, errAPIDown) {
		t.Errorf("LoadRiskState = %v, want the read error", err)
	}
	if err := SaveRiskState(ctx, clientFailingGets(), "costdeck", st); !errors.Is(err, errAPIDown) {
		t.Errorf("SaveRiskState = %v, want the read error", err)
	}
	d := WithOpenRisks(ctx, clientFailingGets(), "costdeck", Digest{Title: "kept"})
	if d.Title != "kept" || d.CriticalRisks != nil {
		t.Errorf("a digest whose risks cannot be read = %+v, want it unchanged", d)
	}
}

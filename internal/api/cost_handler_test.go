package api

import (
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestCostingPricesWhatWasAsked(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	createAll(t, server.Client,
		podWith("shop", "web", corev1.PodRunning, corev1.Container{Name: "app", Resources: requests("1", "2Gi")}),
		podWith("shop", "queued", corev1.PodPending, corev1.Container{Name: "app", Resources: requests("500m", "1Gi")}),
		podWith("shop", "migration", corev1.PodSucceeded, corev1.Container{Name: "job", Resources: requests("4", "8Gi")}),
		podWith("other", "noise", corev1.PodRunning, corev1.Container{Name: "app", Resources: requests("8", "8Gi")}),
	)

	for _, tc := range []struct {
		body       string
		cores, gib float64
	}{
		{`{"targetType":"cluster","totalCpu":8,"totalMemoryGb":32}`, 8, 32},
		{`{"targetType":"node","targetName":"n1","totalCpu":2.5,"totalMemoryGb":4}`, 2.5, 4},
		{`{"targetType":"namespace","targetName":"shop"}`, 1.5, 3},
	} {
		rr := send(t, server, http.MethodPost, "/api/costing", tc.body)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", tc.body, rr.Code, rr.Body.String())
		}
		got := decodeInto[CostResponse](t, rr)
		hourly := tc.cores*cpuCoreHourRate + tc.gib*memoryGiBHourRate
		if !closeTo(got.HourlyCost, hourly) || !closeTo(got.MonthlyCost, hourly*730) || got.Currency != customCurrency || got.DeterminedBy != customRatesBasis {
			t.Errorf("%s = %+v, want %.4f an hour at the custom rates", tc.body, got, hourly)
		}
	}
}

func TestCostingRejectsUnknownTargets(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	for body, want := range map[string]string{
		`{"targetType":"pod","targetName":"web"}`: "targetType must be cluster, node or namespace",
		``:                    "targetType must be cluster, node or namespace",
		`{"targetType":`:      "Invalid request",
		`{"totalCpu":"many"}`: "Invalid request",
	} {
		rr := send(t, server, http.MethodPost, "/api/costing", body)
		if rr.Code != http.StatusBadRequest || errorMessage(t, rr) != want {
			t.Errorf("%q = %d %s, want 400 %q", body, rr.Code, rr.Body.String(), want)
		}
	}
}

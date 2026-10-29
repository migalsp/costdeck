package estimate

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// A pod of 500m and 1Gi costs (0.5 × 0.04 + 1 × 0.005) × 730 = 18.25 a month.
var prices = Prices{
	Rates:             pricing.Rates{CPUCoreHour: 0.04, MemoryGBHour: 0.005, GPUHour: 1, Currency: "USD", Basis: "Custom rates"},
	Storage:           func(class string) float64 { return map[string]float64{"": 0.1, "fast": 0.2}[class] },
	LoadBalancerMonth: 18,
	Nodes:             5,
	UpShare: func(ns string) (float64, string) {
		if ns == "shop-dev" {
			return 0.25, "up 25% of the time on its schedule"
		}
		return 1, ""
	},
}

const pod = `
    spec:
      containers:
        - name: main
          image: app
          resources: {requests: {cpu: 500m, memory: 1Gi}}`

func deployment(ns, name string, replicas int) string {
	return "---\napiVersion: apps/v1\nkind: Deployment\nmetadata: {name: " + name + ", namespace: " + ns + "}\nspec:\n  replicas: " +
		strconv.Itoa(replicas) + "\n  selector: {matchLabels: {app: " + name + "}}\n  template:\n    metadata: {labels: {app: " + name + "}}" + pod + "\n"
}

var base = deployment("shop", "api", 2) + deployment("shop", "same", 1) + deployment("shop", "old", 1) + deployment("shop-dev", "web", 1) + `
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data, namespace: shop}
spec: {resources: {requests: {storage: 10Gi}}}
`

var head = deployment("shop", "api", 4) + deployment("shop", "same", 1) + deployment("shop-dev", "web", 1) + `
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: web, namespace: shop-dev}
spec: {scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: web}, minReplicas: 3, maxReplicas: 9}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data, namespace: shop}
spec: {resources: {requests: {storage: 20Gi}}}
---
apiVersion: v1
kind: List
items:
  - apiVersion: apps/v1
    kind: StatefulSet
    metadata: {name: db}
    spec:
      replicas: 2
      selector: {matchLabels: {app: db}}
      template:
        metadata: {labels: {app: db}}
        spec:
          containers:
            - name: pg
              image: postgres
              resources: {requests: {cpu: 500m, memory: 1Gi}, limits: {nvidia.com/gpu: 1}}
      volumeClaimTemplates:
        - metadata: {name: data}
          spec: {storageClassName: fast, resources: {requests: {storage: 5Gi}}}
  - apiVersion: apps/v1
    kind: DaemonSet
    metadata: {name: agent, namespace: kube-system}
    spec:
      selector: {matchLabels: {app: agent}}
      template:
        metadata: {labels: {app: agent}}
        spec:
          containers:
            - name: agent
              image: agent
              resources: {requests: {cpu: 500m, memory: 1Gi}}
  - apiVersion: v1
    kind: Service
    metadata: {name: shop, namespace: shop}
    spec: {type: LoadBalancer, ports: [{port: 80}]}
  - apiVersion: v1
    kind: ConfigMap
    metadata: {name: ignored}
`

func TestEstimate(t *testing.T) {
	r, err := Estimate([]byte(base), []byte(head), "shop", prices)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Item{}
	for _, it := range r.Items {
		got[it.Kind+"/"+it.Namespace+"/"+it.Name] = it
	}
	want := map[string]float64{
		"Deployment/shop/api":             36.5,            // 2 → 4 pods of 18.25
		"Deployment/shop/old":             -18.25,          // removed
		"Deployment/shop-dev/web":         9.13,            // HPA floor 3, up a quarter of the time: (3 − 1) × 18.25 × 0.25
		"PersistentVolumeClaim/shop/data": 1,               // 10Gi more at 0.1
		"StatefulSet/shop/db":             36.5 + 1460 + 2, // 2 pods, 2 GPUs at 730 each, 10Gi at 0.2
		"DaemonSet/kube-system/agent":     91.25,           // on 5 nodes
		"Service/shop/shop":               18,
	}
	if len(got) != len(want) || r.Unchanged != 1 {
		t.Fatalf("items = %+v, unchanged %d", r.Items, r.Unchanged)
	}
	for k, v := range want {
		if d := got[k].Delta; d < v-0.01 || d > v+0.01 {
			t.Errorf("%s = %v, want %v (%+v)", k, d, v, got[k])
		}
	}
	if r.Items[0].Kind != "StatefulSet" {
		t.Errorf("the biggest change is not first: %s", r.Items[0].Kind)
	}
	if web := got["Deployment/shop-dev/web"]; web.After.Replicas != 3 || web.Note != "up 25% of the time on its schedule" {
		t.Errorf("web = %+v", web)
	}
	if db := got["StatefulSet/shop/db"]; db.After.GPU != 2 || db.After.StorageGiB != 10 || db.After.CPU != "1" || db.After.Memory != "2Gi" {
		t.Errorf("db = %+v", db.After)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "shop-dev/web scales between 3 and 9 replicas") {
		t.Errorf("warnings = %v", r.Warnings)
	}
	if r.Delta != r.After-r.Before || r.Delta != 1636.13 {
		t.Errorf("delta = %v (%v → %v)", r.Delta, r.Before, r.After)
	}

	md := r.Markdown()
	for _, s := range []string{Marker, "### Cost estimate: +$1,636.13 a month", "Deployment `shop/api`: 2 → 4 replicas", "Deployment `shop/old` (removed)",
		"StatefulSet `shop/db` (new)", "**−$18.25**", "1 more object(s) unchanged", "> shop-dev/web scales between 3 and 9 replicas", "at custom rates"} {
		if !strings.Contains(md, s) {
			t.Errorf("markdown has no %q:\n%s", s, md)
		}
	}
}

func TestEstimateCountsNegativeValuesAsZero(t *testing.T) {
	negative := deployment("shop", "offset", -5) + `
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: half, namespace: shop}
spec:
  template:
    spec:
      containers:
        - {name: main, image: app, resources: {requests: {cpu: 500m, memory: 1Gi}}}
        - {name: side, image: app, resources: {requests: {cpu: "-4", memory: -8Gi}, limits: {nvidia.com/gpu: "-2"}}}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data, namespace: shop}
spec: {resources: {requests: {storage: -10Gi}}}
`
	r, err := Estimate(nil, []byte(negative), "default", prices)
	if err != nil {
		t.Fatal(err)
	}
	if r.After != 18.25 || r.Delta != 18.25 {
		t.Errorf("after = %v, delta = %v, want 18.25: only the valid container of half costs anything", r.After, r.Delta)
	}
	for _, it := range r.Items {
		if it.After.Monthly < 0 || it.After.Replicas < 0 || it.After.StorageGiB < 0 || it.After.GPU < 0 {
			t.Errorf("%s/%s is priced below zero: %+v", it.Kind, it.Name, *it.After)
		}
	}
	if len(r.Warnings) != 3 || !strings.Contains(strings.Join(r.Warnings, "\n"), "Deployment shop/offset asks for negative") {
		t.Errorf("warnings = %q, want one for each of the three objects", r.Warnings)
	}
}

func TestEstimateWarnsAboutNegativeValuesItDoesNotPrice(t *testing.T) {
	for _, tc := range []struct{ name, manifest string }{
		{"a negative claim of a StatefulSet with no replicas", `
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: db, namespace: shop}
spec:
  replicas: 0
  template: {spec: {containers: [{name: db, image: db}]}}
  volumeClaimTemplates: [{metadata: {name: data}, spec: {resources: {requests: {storage: -10Gi}}}}]
`},
		{"a negative ephemeral-storage request", `
apiVersion: v1
kind: Pod
metadata: {name: job, namespace: shop}
spec: {containers: [{name: main, image: app, resources: {requests: {cpu: 500m, memory: 1Gi, ephemeral-storage: -1Gi}}}]}
`},
		{"a negative memory limit of an init container", `
apiVersion: v1
kind: Pod
metadata: {name: init, namespace: shop}
spec:
  initContainers: [{name: setup, image: app, resources: {limits: {memory: -1Gi}}}]
  containers: [{name: main, image: app, resources: {requests: {cpu: 500m, memory: 1Gi}}}]
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Estimate(nil, []byte(tc.manifest), "default", prices)
			if err != nil {
				t.Fatal(err)
			}
			if r.After < 0 || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "asks for negative") {
				t.Errorf("after = %v, warnings = %q, want a cost of at least zero and one warning", r.After, r.Warnings)
			}
		})
	}
}

func TestEstimateClampsRolloutReplicas(t *testing.T) {
	rollout := func(replicas string) []byte {
		return []byte("apiVersion: argoproj.io/v1alpha1\nkind: Rollout\nmetadata: {name: web, namespace: shop}\nspec:\n  replicas: " + replicas + "\n  template:" + pod + "\n")
	}
	for _, tc := range []struct {
		replicas string
		want     int32
		warns    bool
	}{
		{"-3", 0, true},
		{"4294967297", math.MaxInt32, false},
		{"2", 2, false},
	} {
		r, err := Estimate(nil, rollout(tc.replicas), "default", prices)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Items) != 1 || r.Items[0].After.Replicas != tc.want {
			t.Errorf("replicas %s: items = %+v, want %d replicas", tc.replicas, r.Items, tc.want)
			continue
		}
		if warned := len(r.Warnings) > 0; warned != tc.warns {
			t.Errorf("replicas %s: warnings = %q, want a warning: %v", tc.replicas, r.Warnings, tc.warns)
		}
	}
}

func TestEstimateWithoutChanges(t *testing.T) {
	r, err := Estimate([]byte(base), []byte(base), "shop", prices)
	if err != nil || len(r.Items) != 0 || r.Delta != 0 {
		t.Fatalf("%+v, %v", r, err)
	}
	if !strings.Contains(r.Markdown(), "### Cost estimate: no change") {
		t.Error(r.Markdown())
	}
	if _, err := Estimate([]byte("kind: [broken"), []byte(base), "shop", prices); err == nil || !strings.HasPrefix(err.Error(), "base:") {
		t.Errorf("broken YAML: %v", err)
	}
	if money(-1234567.891, "EUR") != "−€1,234,567.89" || money(5, "CHF") != "5.00 CHF" {
		t.Errorf("money = %s, %s", money(-1234567.891, "EUR"), money(5, "CHF"))
	}
}

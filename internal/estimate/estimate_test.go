package estimate

import (
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

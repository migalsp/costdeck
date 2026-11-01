package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	storagev1 "k8s.io/api/storage/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

const estimateDeploy = `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: shop-dev}
spec:
  replicas: REPLICAS
  selector: {matchLabels: {app: api}}
  template:
    metadata: {labels: {app: api}}
    spec:
      containers: [{name: main, image: api, resources: {requests: {cpu: "1", memory: 2Gi}}}]
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data}
spec: {resources: {requests: {storage: SIZE}}}
`

// estimateNamespace is where objects without a namespace go.
const estimateNamespace = "shop"

func TestEstimateEndpoint(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	// 1 core at 0.05 and 2 GiB at 0.01 an hour: 51.10 a pod-month; storage 0.10 a GiB.
	if rr := send(t, server, "PUT", "/api/settings", `{"pricing":{"cpuCoreHour":"0.05","memoryGiBHour":"0.01","storageGiBMonth":"0.1","currency":"`+remoteCurrency+`"}}`); rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	_ = server.Client.Create(ctx, &storagev1.StorageClass{Name: "standard", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}, Provisioner: "ebs.csi.aws.com"})
	g := &finopsv1.ScalingGroup{Name: "dev", Namespace: "costdeck", Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop-dev"},
		Schedules: []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "11:59", Timezone: "UTC"}}}}
	if err := server.Client.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	manifests := func(replicas, size string) string {
		return strings.NewReplacer("REPLICAS", replicas, "SIZE", size).Replace(estimateDeploy)
	}
	body, _ := json.Marshal(map[string]string{"base": manifests("1", "10Gi"), "head": manifests("3", "30Gi"), "namespace": estimateNamespace})
	rr := send(t, server, "POST", "/api/estimate", string(body))
	var got estimateResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if rr.Code != http.StatusOK || got.Currency != remoteCurrency || len(got.Items) != 2 {
		t.Fatalf("estimate: %d %s", rr.Code, rr.Body.String())
	}
	for _, it := range got.Items {
		switch it.Kind {
		case "Deployment":
			// Two more pods of 51.10, up half of the time on the schedule.
			if it.Delta < 51 || it.Delta > 51.2 || !strings.Contains(it.Note, "up 50% of the time on schedule dev") {
				t.Errorf("deployment = %+v", it)
			}
		case "PersistentVolumeClaim":
			if it.Namespace != estimateNamespace || it.Delta != 2 {
				t.Errorf("claim = %+v", it)
			}
		}
	}
	if !strings.HasPrefix(got.Markdown, "<!-- costdeck-estimate -->\n### Cost estimate: +€53.") {
		t.Errorf("markdown = %s", got.Markdown)
	}
	if rr := send(t, server, "POST", "/api/estimate", `{"base":"kind: [","head":""}`); rr.Code != http.StatusBadRequest {
		t.Errorf("broken manifests: %d", rr.Code)
	}
}

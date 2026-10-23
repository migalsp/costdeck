package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/rightsizing"
)

func rightsizingFixture(t *testing.T) *Server {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	mk := func(name string, annotations map[string]string) {
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Annotations: annotations}, Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "main", Image: name, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{"cpu": resource.MustParse("2"), "memory": resource.MustParse("4Gi")}}}}}},
		}}
		if err := server.Client.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	mk("api", nil)
	mk("managed", map[string]string{"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:shop/managed"})
	advice := func(name string) rightsizing.WorkloadAdvice {
		return rightsizing.WorkloadAdvice{Kind: "Deployment", Name: name, Replicas: 1, MonthlySavings: 50, Containers: []rightsizing.ContainerAdvice{{Name: "main",
			CPU:    rightsizing.ResourceAdvice{Request: "2", Observed: "100m", Recommended: "150m", Action: rightsizing.ActionReduce},
			Memory: rightsizing.ResourceAdvice{Request: "4Gi", Observed: "300Mi", Recommended: "384Mi", Action: rightsizing.ActionReduce}}}}
	}
	server.recommendationCache.put("shop", rightsizing.Report{Namespace: "shop", Currency: "USD", Basis: "A single reading.",
		Workloads: []rightsizing.WorkloadAdvice{advice("api"), advice("managed")}})
	return server
}

// originalRequests are the workloads' requests before any change.
const originalRequests = "2/4Gi"

func requestsOf(t *testing.T, server *Server, name string) string {
	t.Helper()
	var d appsv1.Deployment
	_ = server.Client.Get(context.Background(), client.ObjectKey{Namespace: "shop", Name: name}, &d)
	r := d.Spec.Template.Spec.Containers[0].Resources.Requests
	return r.Cpu().String() + "/" + r.Memory().String()
}

func TestApplyAndRevertRightsizing(t *testing.T) {
	server := rightsizingFixture(t)
	if rr := send(t, server, "POST", "/api/namespaces/shop/rightsizing/apply", `{"kind":"Deployment","name":"api"}`); rr.Code != http.StatusForbidden ||
		!strings.Contains(rr.Body.String(), "Settings → Right-sizing") {
		t.Errorf("applying while it is off: %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"rightsizing":{"apply":true,"guardMinutes":15}}`); rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	rr := send(t, server, "POST", "/api/namespaces/shop/rightsizing/apply", `{"kind":"Deployment","name":"api"}`)
	if rr.Code != http.StatusOK || requestsOf(t, server, "api") != "150m/384Mi" {
		t.Fatalf("apply: %d %s → %s", rr.Code, rr.Body.String(), requestsOf(t, server, "api"))
	}
	// Applying drops that workload's advice and keeps the rest: no usage is asked for again
	// while it rolls out.
	if r, _ := server.recommendationCache.get("shop"); len(r.Workloads) != 1 || r.Workloads[0].Name != "managed" || r.MonthlySavings != 50 {
		t.Errorf("advice after applying = %+v", r)
	}
	if rr := send(t, server, "POST", "/api/namespaces/shop/rightsizing/apply", `{"kind":"Deployment","name":"managed"}`); rr.Code != http.StatusConflict ||
		!strings.Contains(rr.Body.String(), "pull request") || requestsOf(t, server, "managed") != originalRequests {
		t.Errorf("a GitOps workload: %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(t, server, "POST", "/api/namespaces/shop/rightsizing/apply", `{"kind":"Deployment","name":"ghost"}`); rr.Code != http.StatusNotFound {
		t.Errorf("a workload without advice: %d", rr.Code)
	}
	var got rightsizingChanges
	_ = json.Unmarshal(send(t, server, "GET", "/api/rightsizing/changes?namespace=shop", "").Body.Bytes(), &got)
	if !got.ApplyEnabled || got.GuardMinutes != 15 || got.Repository != "" || len(got.Changes) != 1 || got.Changes[0].State != "applied" ||
		got.Changes[0].Containers[0].Original.CPURequest != "2" {
		t.Fatalf("changes = %+v", got)
	}
	if rr := send(t, server, "POST", "/api/namespaces/shop/rightsizing/revert", `{"kind":"Deployment","name":"api"}`); rr.Code != http.StatusOK || requestsOf(t, server, "api") != originalRequests {
		t.Errorf("revert: %d %s → %s", rr.Code, rr.Body.String(), requestsOf(t, server, "api"))
	}
	var settings struct {
		Rightsizing RightsizingSettings `json:"rightsizing"`
	}
	_ = json.Unmarshal(send(t, server, "GET", "/api/settings", "").Body.Bytes(), &settings)
	if !settings.Rightsizing.Apply || settings.Rightsizing.GuardMinutes != 15 {
		t.Errorf("settings = %+v", settings.Rightsizing)
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"rightsizing":{"guardMinutes":1}}`); rr.Code != http.StatusBadRequest {
		t.Errorf("a one-minute watch: %d", rr.Code)
	}
}

func TestProposeRightsizing(t *testing.T) {
	server := rightsizingFixture(t)
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: api}\nspec:\n  template:\n    spec:\n      containers:\n        - name: main\n          resources:\n            requests: {cpu: \"2\", memory: 4Gi}\n"
	var pr, put map[string]any
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gt" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/repos/acme/apps":
			_, _ = w.Write([]byte(`{}`))
		case "GET /api/v1/repos/acme/apps/git/trees/main":
			_, _ = w.Write([]byte(`{"tree":[{"path":"shop/api.yaml","type":"blob"}]}`))
		case "GET /api/v1/repos/acme/apps/contents/shop/api.yaml":
			_, _ = w.Write([]byte(`{"content":"` + base64.StdEncoding.EncodeToString([]byte(manifest)) + `","sha":"s1"}`))
		case "PUT /api/v1/repos/acme/apps/contents/shop/api.yaml":
			_ = json.NewDecoder(r.Body).Decode(&put)
			_, _ = w.Write([]byte(`{}`))
		case "POST /api/v1/repos/acme/apps/pulls":
			_ = json.NewDecoder(r.Body).Decode(&pr)
			_, _ = w.Write([]byte(`{"html_url":"http://gitea/acme/apps/pulls/1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gitea.Close()
	server.HTTPClient = gitea.Client()

	if rr := send(t, server, "POST", "/api/namespaces/shop/rightsizing/pull-request", `{"kind":"Deployment","name":"api"}`); rr.Code != http.StatusForbidden {
		t.Errorf("without a repository: %d", rr.Code)
	}
	for body, why := range map[string]string{
		`{"rightsizing":{"git":{"provider":"gitea","repository":"acme/apps","token":"gt"}}}`:                                   "a Gitea without its API URL",
		`{"rightsizing":{"git":{"provider":"bitbucket","repository":"acme/apps","token":"gt"}}}`:                               "an unknown provider",
		`{"rightsizing":{"git":{"provider":"github","repository":"acme/apps"}}}`:                                               "no token",
		`{"rightsizing":{"git":{"provider":"github","repository":"acme/apps","apiUrl":"http://169.254.169.254","token":"x"}}}`: "a metadata address",
	} {
		if rr := send(t, server, "PUT", "/api/settings", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", why, rr.Code, rr.Body.String())
		}
	}
	git := `{"provider":"gitea","repository":"acme/apps","apiUrl":"` + gitea.URL + `/api/v1","token":"gt"}`
	if rr := send(t, server, "POST", "/api/settings/providers/git/test", git); !strings.Contains(rr.Body.String(), `"connected":true`) {
		t.Errorf("test: %s", rr.Body.String())
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"rightsizing":{"git":`+git+`}}`); rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	rr := send(t, server, "POST", "/api/namespaces/shop/rightsizing/pull-request", `{"kind":"Deployment","name":"api"}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"pullRequest":"http://gitea/acme/apps/pulls/1"`) || !strings.Contains(rr.Body.String(), `"state":"proposed"`) {
		t.Fatalf("propose: %d %s", rr.Code, rr.Body.String())
	}
	raw, _ := base64.StdEncoding.DecodeString(put["content"].(string))
	if !strings.Contains(string(raw), "cpu: 150m") || !strings.Contains(string(raw), "memory: 384Mi") || put["sha"] != "s1" {
		t.Errorf("commit = %s (%v)", raw, put)
	}
	if pr["title"] != "Right-size Deployment shop/api" || !strings.Contains(pr["body"].(string), "| main | 2 → **150m** (used 100m) |") {
		t.Errorf("pull request = %v", pr)
	}
	if requestsOf(t, server, "api") != originalRequests {
		t.Error("a pull request changed the workload itself")
	}
}

// Advice with containers that have no usage yet is worked out again soon, not in five
// minutes: the pods of a new namespace get their data within a minute or two.
func TestPartialAdviceIsKeptBriefly(t *testing.T) {
	var c recommendationCache
	unknown := rightsizing.Report{Workloads: []rightsizing.WorkloadAdvice{{Name: "api", Containers: []rightsizing.ContainerAdvice{{Name: "main",
		CPU: rightsizing.ResourceAdvice{Action: rightsizing.ActionUnknown}, Memory: rightsizing.ResourceAdvice{Action: rightsizing.ActionUnknown}}}}}}
	c.put("new", unknown)
	c.put("known", rightsizing.Report{})
	if c.entries["new"].ttl != partialTTL || c.entries["known"].ttl != recommendationTTL {
		t.Errorf("ttls = %v, %v", c.entries["new"].ttl, c.entries["known"].ttl)
	}
}

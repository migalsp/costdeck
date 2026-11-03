package scaling

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const sleepNS = "shop-dev"

var (
	routeGVK = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}
	grantGVK = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1beta1", Kind: "ReferenceGrant"}
)

func route(ns, name string, rules []any, hosts ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"rules": rules}}}
	if len(hosts) > 0 {
		h := make([]any, len(hosts))
		for i, s := range hosts {
			h[i] = s
		}
		u.Object["spec"].(map[string]any)["hostnames"] = h
	}
	u.SetGroupVersionKind(routeGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func TestSleepRoutesAndWakeThemExactly(t *testing.T) {
	ctx := context.Background()
	rules := []any{
		map[string]any{
			"matches":     []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/api"}}},
			"filters":     []any{map[string]any{"type": "URLRewrite", "urlRewrite": map[string]any{"path": map[string]any{"type": "ReplacePrefixMatch", "replacePrefixMatch": "/"}}}},
			"backendRefs": []any{map[string]any{"name": "api", "port": int64(8080), "weight": int64(90)}, map[string]any{"name": "api-canary", "port": int64(8080), "weight": int64(10)}},
		},
		map[string]any{"backendRefs": []any{map[string]any{"name": "web", "port": int64(80)}}},
	}
	shop := route(sleepNS, "shop", rules, "shop-dev.example.com")
	other := route("billing", "billing", []any{map[string]any{"backendRefs": []any{map[string]any{"name": "b", "port": int64(80)}}}})
	e := hibernateEngine(t, []schema.GroupVersionKind{routeGVK, grantGVK}, nil, shop, other)
	page := SleepBackend{Name: "costdeck-operator-api", Namespace: "costdeck", Port: 8082}
	original := reread(t, e, shop).Object["spec"]

	if err := e.SyncSleepGrant(ctx, page, sleepNS); err != nil {
		t.Fatal(err)
	}
	n, err := e.SleepRoutes(ctx, sleepNS, page)
	if err != nil || n != 1 {
		t.Fatalf("SleepRoutes = %d, %v", n, err)
	}
	asleep := reread(t, e, shop)
	gotRules, _, _ := unstructured.NestedSlice(asleep.Object, "spec", "rules")
	for i, raw := range gotRules {
		r := raw.(map[string]any)
		refs := r["backendRefs"].([]any)
		ref := refs[0].(map[string]any)
		if len(refs) != 1 || ref["name"] != page.Name || ref["namespace"] != "costdeck" || ref["port"] != int64(8082) {
			t.Errorf("rule %d backends = %v", i, refs)
		}
		header := r["filters"].([]any)[0].(map[string]any)["requestHeaderModifier"].(map[string]any)["set"].([]any)[0].(map[string]any)
		if header["name"] != SleepingHeader || header["value"] != sleepNS {
			t.Errorf("rule %d filters = %v", i, r["filters"])
		}
	}
	if m := gotRules[0].(map[string]any)["matches"]; !reflect.DeepEqual(m, rules[0].(map[string]any)["matches"]) {
		t.Errorf("matches changed: %v", m)
	}
	version := asleep.GetResourceVersion()
	if n, _ := e.SleepRoutes(ctx, sleepNS, page); n != 1 || reread(t, e, shop).GetResourceVersion() != version {
		t.Errorf("a route already asleep was changed again, or not counted: %d", n)
	}
	if ns, _ := e.SleepingNamespaces(ctx); strings.Join(ns, ",") != sleepNS {
		t.Errorf("sleeping = %v", ns)
	}

	grant := &unstructured.Unstructured{}
	grant.SetGroupVersionKind(grantGVK)
	if err := e.Client.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: SleepGrantName}, grant); err != nil {
		t.Fatalf("no ReferenceGrant: %v", err)
	}
	from, _, _ := unstructured.NestedSlice(grant.Object, "spec", "from")
	to, _, _ := unstructured.NestedSlice(grant.Object, "spec", "to")
	if len(from) != 1 || from[0].(map[string]any)["namespace"] != sleepNS || from[0].(map[string]any)["kind"] != "HTTPRoute" || to[0].(map[string]any)["name"] != page.Name {
		t.Errorf("grant = %v", grant.Object["spec"])
	}

	if n, err := e.WakeRoutes(ctx, sleepNS); err != nil || n != 1 {
		t.Fatalf("WakeRoutes = %d, %v", n, err)
	}
	woken := reread(t, e, shop)
	if !reflect.DeepEqual(woken.Object["spec"], original) || woken.GetAnnotations()[SleepingAnnotation] != "" {
		t.Errorf("not restored exactly:\n got %v\nwant %v", woken.Object["spec"], original)
	}
	if err := e.SyncSleepGrant(ctx, page); err != nil {
		t.Fatal(err)
	}
	if err := e.Client.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: SleepGrantName}, grant); err == nil {
		t.Error("the grant stays although nothing sleeps")
	}
	if hosts, _ := e.RouteHostnames(ctx, sleepNS); strings.Join(hosts, ",") != "shop-dev.example.com" {
		t.Errorf("hostnames = %v", hosts)
	}
}

func TestSleepPageWithoutGatewayAPI(t *testing.T) {
	e := hibernateEngine(t, nil, nil)
	page := SleepBackend{Name: "x", Namespace: "costdeck", Port: 1}
	if n, err := e.SleepRoutes(context.Background(), "shop", page); n != 0 || err != nil {
		t.Errorf("SleepRoutes = %d, %v", n, err)
	}
	if err := e.SyncSleepGrant(context.Background(), page, "shop"); err != nil {
		t.Errorf("SyncSleepGrant = %v", err)
	}
}

func TestRoutesAsleepOnAnEarlierServiceMoveToTheSleepPageService(t *testing.T) {
	ctx := context.Background()
	original := []any{map[string]any{"backendRefs": []any{map[string]any{"name": "web", "port": int64(80)}}}}
	shop := route(sleepNS, "shop", original)
	e := hibernateEngine(t, []schema.GroupVersionKind{routeGVK, grantGVK}, nil, shop)
	earlier := SleepBackend{Name: "costdeck-operator-api", Namespace: "costdeck", Port: 8082}
	current := SleepBackend{Name: "costdeck-operator-sleep", Namespace: "costdeck", Port: 80}
	if _, err := e.SleepRoutes(ctx, sleepNS, earlier); err != nil {
		t.Fatal(err)
	}
	saved := reread(t, e, shop).GetAnnotations()[SleepingAnnotation]

	if n, err := e.SleepRoutes(ctx, sleepNS, current); err != nil || n != 1 {
		t.Fatalf("SleepRoutes = %d, %v", n, err)
	}
	moved := reread(t, e, shop)
	rules, _, _ := unstructured.NestedSlice(moved.Object, "spec", "rules")
	ref := rules[0].(map[string]any)["backendRefs"].([]any)[0].(map[string]any)
	if ref["name"] != current.Name || ref["port"] != current.Port {
		t.Errorf("backend = %v, want the sleep page Service %s:%d", ref, current.Name, current.Port)
	}
	if got := moved.GetAnnotations()[SleepingAnnotation]; got != saved {
		t.Errorf("the saved backends changed when the route moved: %s, want %s", got, saved)
	}
	version := moved.GetResourceVersion()
	if _, err := e.SleepRoutes(ctx, sleepNS, current); err != nil {
		t.Fatal(err)
	}
	if reread(t, e, shop).GetResourceVersion() != version {
		t.Error("a route already on the current Service was written again")
	}
	if _, err := e.WakeRoutes(ctx, sleepNS); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedSlice(reread(t, e, shop).Object, "spec", "rules"); !reflect.DeepEqual(got, original) {
		t.Errorf("woken rules = %v, want the application's own %v", got, original)
	}
}

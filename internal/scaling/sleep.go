package scaling

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// While a namespace is down, its Gateway API HTTPRoutes can point at Cost Deck's sleep
// page instead of its stopped Services: whoever opens the application sees that it is
// asleep and how to wake it, rather than a gateway error. Nothing of Cost Deck sits in the
// path of the traffic while the namespace is up. The page is served by Cost Deck's API
// Service in its own namespace, which a ReferenceGrant there opens to the sleeping
// namespaces' routes.

const (
	// SleepingAnnotation holds, on a route pointed at the sleep page, its rules' original
	// backends and filters.
	SleepingAnnotation = "costdeck.io/sleeping"
	// SleepingHeader tells the sleep page which namespace a request was meant for.
	SleepingHeader = "X-Costdeck-Sleeping"
	// SleepGrantName is the ReferenceGrant that lets the routes reach the sleep page.
	SleepGrantName = "costdeck-sleep-page"
)

var (
	httpRouteGK      = schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}
	referenceGrantGK = schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "ReferenceGrant"}
)

// SleepBackend is the Service serving the sleep page.
type SleepBackend struct {
	Name      string
	Namespace string
	Port      int64
}

// savedRule is what the sleep page replaces in one rule.
type savedRule struct {
	BackendRefs any `json:"backendRefs,omitempty"`
	Filters     any `json:"filters,omitempty"`
}

// mapping finds the served version of a Gateway API kind, or false when it is not
// installed.
func (e *Engine) mapping(gk schema.GroupKind, versions ...string) (schema.GroupVersionKind, bool, error) {
	m, err := e.Client.RESTMapper().RESTMapping(gk, versions...)
	if meta.IsNoMatchError(err) {
		return schema.GroupVersionKind{}, false, nil
	}
	if err != nil {
		return schema.GroupVersionKind{}, false, err
	}
	return m.GroupVersionKind, true, nil
}

func (e *Engine) routes(ctx context.Context, ns string) ([]unstructured.Unstructured, error) {
	gvk, ok, err := e.mapping(httpRouteGK, "v1", "v1beta1")
	if err != nil || !ok {
		return nil, err
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind("HTTPRouteList"))
	opts := []client.ListOption{}
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := e.Client.List(ctx, list, opts...); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// SleepRoutes points every HTTPRoute of a namespace at the sleep page and returns how many
// show it now. Routes already asleep are left as they are.
func (e *Engine) SleepRoutes(ctx context.Context, ns string, page SleepBackend) (int, error) {
	routes, err := e.routes(ctx, ns)
	if err != nil {
		return 0, err
	}
	asleep := 0
	for i := range routes {
		r := &routes[i]
		if r.GetAnnotations()[SleepingAnnotation] != "" {
			asleep++
			continue
		}
		rules, _, _ := unstructured.NestedSlice(r.Object, "spec", "rules")
		saved := make([]savedRule, len(rules))
		for j, raw := range rules {
			rule, _ := raw.(map[string]any)
			saved[j] = savedRule{BackendRefs: rule["backendRefs"], Filters: rule["filters"]}
			rule["backendRefs"] = []any{map[string]any{"group": "", "kind": "Service", "name": page.Name, "namespace": page.Namespace, "port": page.Port}}
			rule["filters"] = []any{map[string]any{"type": "RequestHeaderModifier",
				"requestHeaderModifier": map[string]any{"set": []any{map[string]any{"name": SleepingHeader, "value": ns}}}}}
			rules[j] = rule
		}
		raw, err := json.Marshal(saved)
		if err != nil {
			return asleep, err
		}
		if err := e.patchRoute(ctx, r, rules, string(raw)); err != nil {
			return asleep, fmt.Errorf("point HTTPRoute %s/%s at the sleep page: %w", ns, r.GetName(), err)
		}
		asleep++
	}
	return asleep, nil
}

// WakeRoutes puts back what SleepRoutes replaced and returns how many routes it restored.
func (e *Engine) WakeRoutes(ctx context.Context, ns string) (int, error) {
	routes, err := e.routes(ctx, ns)
	if err != nil {
		return 0, err
	}
	changed := 0
	for i := range routes {
		r := &routes[i]
		raw := r.GetAnnotations()[SleepingAnnotation]
		if raw == "" {
			continue
		}
		var saved []savedRule
		if err := json.Unmarshal([]byte(raw), &saved); err != nil {
			return changed, fmt.Errorf("HTTPRoute %s/%s: unreadable %s: %w", ns, r.GetName(), SleepingAnnotation, err)
		}
		rules, _, _ := unstructured.NestedSlice(r.Object, "spec", "rules")
		for j, rawRule := range rules {
			rule, _ := rawRule.(map[string]any)
			delete(rule, "backendRefs")
			delete(rule, "filters")
			if j < len(saved) {
				if saved[j].BackendRefs != nil {
					rule["backendRefs"] = saved[j].BackendRefs
				}
				if saved[j].Filters != nil {
					rule["filters"] = saved[j].Filters
				}
			}
			rules[j] = rule
		}
		if err := e.patchRoute(ctx, r, rules, ""); err != nil {
			return changed, fmt.Errorf("restore HTTPRoute %s/%s: %w", ns, r.GetName(), err)
		}
		changed++
	}
	return changed, nil
}

// patchRoute replaces a route's rules and sets (or, with "", removes) the sleeping mark.
// A merge patch replaces lists whole, which is what restoring them needs.
func (e *Engine) patchRoute(ctx context.Context, r *unstructured.Unstructured, rules []any, mark string) error {
	var annotation any = mark
	if mark == "" {
		annotation = nil
	}
	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{SleepingAnnotation: annotation}},
		"spec":     map[string]any{"rules": rules},
	})
	if err != nil {
		return err
	}
	return e.Client.Patch(ctx, r, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager))
}

// SleepingNamespaces lists the namespaces with a route on the sleep page.
func (e *Engine) SleepingNamespaces(ctx context.Context) ([]string, error) {
	routes, err := e.routes(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range routes {
		if r.GetAnnotations()[SleepingAnnotation] != "" && !slices.Contains(out, r.GetNamespace()) {
			out = append(out, r.GetNamespace())
		}
	}
	sort.Strings(out)
	return out, nil
}

// SyncSleepGrant lets the routes of the sleeping namespaces, plus extra ones about to
// sleep, reach the sleep page: one ReferenceGrant next to its Service, removed when no
// namespace sleeps.
func (e *Engine) SyncSleepGrant(ctx context.Context, page SleepBackend, extra ...string) error {
	gvk, ok, err := e.mapping(referenceGrantGK, "v1", "v1beta1")
	if err != nil || !ok {
		return err
	}
	namespaces, err := e.SleepingNamespaces(ctx)
	if err != nil {
		return err
	}
	for _, ns := range extra {
		if !slices.Contains(namespaces, ns) {
			namespaces = append(namespaces, ns)
		}
	}
	sort.Strings(namespaces)
	grant := &unstructured.Unstructured{}
	grant.SetGroupVersionKind(gvk)
	grant.SetNamespace(page.Namespace)
	grant.SetName(SleepGrantName)
	if len(namespaces) == 0 {
		return client.IgnoreNotFound(e.Client.Delete(ctx, grant))
	}
	from := make([]any, 0, len(namespaces))
	for _, ns := range namespaces {
		from = append(from, map[string]any{"group": httpRouteGK.Group, "kind": httpRouteGK.Kind, "namespace": ns})
	}
	spec := map[string]any{"from": from, "to": []any{map[string]any{"group": "", "kind": "Service", "name": page.Name}}}
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(gvk)
	err = e.Client.Get(ctx, client.ObjectKeyFromObject(grant), current)
	switch {
	case apierrors.IsNotFound(err):
		grant.Object["spec"] = spec
		grant.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "costdeck"})
		return e.Client.Create(ctx, grant, client.FieldOwner(FieldManager))
	case err != nil:
		return err
	}
	raw, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return err
	}
	return e.Client.Patch(ctx, current, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager))
}

// RouteHostnames lists the hostnames of a namespace's HTTPRoutes, to which a wake may
// send the browser back.
func (e *Engine) RouteHostnames(ctx context.Context, ns string) ([]string, error) {
	routes, err := e.routes(ctx, ns)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range routes {
		hosts, _, _ := unstructured.NestedStringSlice(r.Object, "spec", "hostnames")
		for _, h := range hosts {
			if !slices.Contains(out, h) {
				out = append(out, h)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

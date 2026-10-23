package scaling

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// Services that operators run, such as databases, are not Deployments or StatefulSets
// CostDeck can scale itself: their operator would scale them straight back. A hibernate rule
// asks the operator instead, through the custom resource, and leaves the operator's own
// workloads alone (an owner: exclusion). Services stop after a namespace's workloads and
// start before them, so applications never run without their database.

const (
	// HibernatedAnnotation marks a resource CostDeck stopped ("stopped"), so it starts only
	// those again, and one it is starting until it is ready ("starting").
	HibernatedAnnotation = "costdeck.io/hibernated"

	hibernatedStopped  = "stopped"
	hibernatedStarting = "starting"

	cnpgHibernation = "cnpg.io/hibernation"

	presetCNPG     = "cnpg"
	presetZalando  = "zalando-postgres"
	presetStrimzi  = "strimzi"
	readyCondition = "Ready"
	statusTrue     = string(metav1.ConditionTrue)
)

// forget drops CostDeck's mark from a service.
var forget = map[string]any{"metadata": map[string]any{"annotations": map[string]any{HibernatedAnnotation: nil}}}

// hibernateKind is one kind of resource a rule stops.
type hibernateKind struct {
	gk       schema.GroupKind
	versions []string // the first one the cluster serves is used
}

// kindsOf expands a rule into the kinds it covers.
func kindsOf(r finopsv1.HibernateRule) []hibernateKind {
	switch r.Preset {
	case presetCNPG:
		return []hibernateKind{{gk: schema.GroupKind{Group: "postgresql.cnpg.io", Kind: "Cluster"}, versions: []string{"v1"}}}
	case presetZalando:
		return []hibernateKind{{gk: schema.GroupKind{Group: "acid.zalan.do", Kind: "postgresql"}, versions: []string{"v1"}}}
	case presetStrimzi:
		// v1 since Strimzi 0.49; v1beta2 before, and gone since 1.0.
		var out []hibernateKind
		for _, k := range []string{"KafkaConnect", "KafkaMirrorMaker2", "KafkaBridge"} {
			out = append(out, hibernateKind{gk: schema.GroupKind{Group: "kafka.strimzi.io", Kind: k}, versions: []string{"v1", "v1beta2"}})
		}
		return out
	}
	gv, err := schema.ParseGroupVersion(r.APIVersion)
	if err != nil {
		return nil
	}
	return []hibernateKind{{gk: schema.GroupKind{Group: gv.Group, Kind: r.Kind}, versions: []string{gv.Version}}}
}

// ValidateHibernate checks hibernate rules beyond what the CRD can: the fields of a rule
// without a preset, and the name globs.
func ValidateHibernate(rules []finopsv1.HibernateRule) error {
	for i, r := range rules {
		at := fmt.Sprintf("hibernate rule %d", i+1)
		if r.Preset != "" {
			if r.Preset != presetCNPG && r.Preset != presetZalando && r.Preset != presetStrimzi {
				return fmt.Errorf("%s: unknown preset %q (cnpg, zalando-postgres or strimzi)", at, r.Preset)
			}
			if r.APIVersion != "" || r.Kind != "" || r.ReplicasField != "" || r.ReadyField != "" {
				return fmt.Errorf("%s: a preset takes no apiVersion, kind, replicasField or readyField", at)
			}
		} else {
			gv, err := schema.ParseGroupVersion(r.APIVersion)
			switch {
			case r.APIVersion == "" || r.Kind == "" || r.ReplicasField == "":
				return fmt.Errorf("%s: set a preset, or apiVersion, kind and replicasField", at)
			case err != nil || gv.Group == "" || gv.Version == "":
				return fmt.Errorf("%s: apiVersion %q must be group/version, such as redis.example.com/v1", at, r.APIVersion)
			case !fieldPath(r.ReplicasField) || (r.ReadyField != "" && !fieldPath(r.ReadyField)):
				return fmt.Errorf("%s: replicasField and readyField are dotted paths such as spec.replicas", at)
			}
		}
		for _, g := range r.Names {
			if _, err := path.Match(g, ""); err != nil || g == "" {
				return fmt.Errorf("%s: %q is not a name or glob", at, g)
			}
		}
	}
	return nil
}

// fieldPath reports whether s is a dotted path outside metadata, such as spec.replicas.
func fieldPath(s string) bool {
	keys := strings.Split(s, ".")
	return len(keys) > 1 && keys[0] != "metadata" && !slices.Contains(keys, "")
}

// ExclusionsWith adds to a schedule's exclusions the owner: entries that leave the
// workloads of hibernated services to their operators.
func ExclusionsWith(exclusions []string, rules []finopsv1.HibernateRule) []string {
	out := slices.Clone(exclusions)
	for _, r := range rules {
		for _, k := range kindsOf(r) {
			if ex := "owner:" + k.gk.Kind; !slices.Contains(out, ex) {
				out = append(out, ex)
			}
		}
	}
	return out
}

// ScaleWithServices scales a namespace like ScaleTarget, together with the services the
// rules cover: they start before its workloads and stop after them. exclusions must
// already include the rules' own (ExclusionsWith).
func (e *Engine) ScaleWithServices(ctx context.Context, ns string, active bool, sequence, exclusions []string, rules []finopsv1.HibernateRule, originalReplicas map[string]int32, timeoutPassed bool) (map[string]int32, bool, error) {
	if active {
		if ready, err := e.HibernateServices(ctx, ns, true, rules); err != nil || !ready {
			return originalReplicas, false, err
		}
	}
	originals, ready, err := e.ScaleTarget(ctx, ns, active, sequence, exclusions, originalReplicas, timeoutPassed)
	if err != nil || !ready || active {
		return originals, ready, err
	}
	ready, err = e.HibernateServices(ctx, ns, false, rules)
	return originals, ready, err
}

// HibernatedService is one resource a hibernate rule covers.
type HibernatedService struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Running is false when it is stopped, by CostDeck (StoppedByCostDeck) or by someone
	// else, whose stop CostDeck leaves alone.
	Running           bool `json:"running"`
	StoppedByCostDeck bool `json:"stoppedByCostDeck,omitempty"`
}

// HibernateTargets lists the resources the rules cover in a namespace. Kinds that are not
// installed are skipped; any other error, such as a missing permission, is returned.
func (e *Engine) HibernateTargets(ctx context.Context, ns string, rules []finopsv1.HibernateRule) ([]*unstructured.Unstructured, []finopsv1.HibernateRule, error) {
	var objs []*unstructured.Unstructured
	var owners []finopsv1.HibernateRule
	seen := map[string]bool{} // rules that overlap cover a resource once
	for _, r := range rules {
		for _, k := range kindsOf(r) {
			mapping, err := e.Client.RESTMapper().RESTMapping(k.gk, k.versions...)
			if meta.IsNoMatchError(err) {
				continue // the operator is not installed
			}
			if err != nil {
				return nil, nil, fmt.Errorf("find %s: %w", k.gk.Kind, err)
			}
			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(mapping.GroupVersionKind.GroupVersion().WithKind(k.gk.Kind + "List"))
			if err := e.Client.List(ctx, list, client.InNamespace(ns)); err != nil {
				return nil, nil, fmt.Errorf("list %s: %w", k.gk.Kind, err)
			}
			for i := range list.Items {
				id := k.gk.Kind + "/" + list.Items[i].GetName()
				if !seen[id] && nameMatches(list.Items[i].GetName(), r.Names) {
					seen[id] = true
					objs = append(objs, &list.Items[i])
					owners = append(owners, r)
				}
			}
		}
	}
	return objs, owners, nil
}

func nameMatches(name string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		if ok, err := path.Match(g, name); err == nil && ok {
			return true
		}
	}
	return false
}

// HibernateServices stops (active false) or starts the services the rules cover and says
// whether all of them have got there. It starts only what it stopped itself.
func (e *Engine) HibernateServices(ctx context.Context, ns string, active bool, rules []finopsv1.HibernateRule) (bool, error) {
	if len(rules) == 0 {
		return true, nil
	}
	objs, owners, err := e.HibernateTargets(ctx, ns, rules)
	if err != nil {
		return false, err
	}
	ready := true
	for i, obj := range objs {
		r := owners[i]
		state := obj.GetAnnotations()[HibernatedAnnotation]
		var err error
		switch {
		case active && state == "":
			continue // never stopped by CostDeck: not ours to start
		case active && state == hibernatedStopped:
			err = e.setService(ctx, obj, r, true)
		case !active && state == "" && e.stoppedAlready(obj, r):
			continue // stopped by someone else
		case !active:
			err = e.setService(ctx, obj, r, false)
		}
		if err != nil {
			return false, fmt.Errorf("%s %s/%s: %w", obj.GetKind(), ns, obj.GetName(), err)
		}
		switch {
		case !e.serviceReady(ctx, obj, r, active):
			ready = false
		case active:
			// Running again: forget it, so a stop by someone else stays theirs.
			if err := e.patchService(ctx, obj, forget); err != nil {
				return false, err
			}
		}
	}
	return ready, nil
}

// ReleaseServices starts the services CostDeck stopped in a namespace it no longer
// schedules, and forgets them without waiting until they are up.
func (e *Engine) ReleaseServices(ctx context.Context, ns string, rules []finopsv1.HibernateRule) error {
	objs, owners, err := e.HibernateTargets(ctx, ns, rules)
	if err != nil {
		return err
	}
	for i, obj := range objs {
		state := obj.GetAnnotations()[HibernatedAnnotation]
		if state == hibernatedStopped {
			if err := e.setService(ctx, obj, owners[i], true); err != nil {
				return err
			}
		}
		if state != "" {
			if err := e.patchService(ctx, obj, forget); err != nil {
				return err
			}
		}
	}
	return nil
}

// Services lists the services the rules cover in a namespace, sorted, for the preview.
func (e *Engine) Services(ctx context.Context, ns string, rules []finopsv1.HibernateRule) ([]HibernatedService, error) {
	objs, owners, err := e.HibernateTargets(ctx, ns, rules)
	if err != nil {
		return nil, err
	}
	out := make([]HibernatedService, 0, len(objs))
	for i, obj := range objs {
		out = append(out, HibernatedService{Kind: obj.GetKind(), Name: obj.GetName(), Running: !e.stoppedAlready(obj, owners[i]),
			StoppedByCostDeck: obj.GetAnnotations()[HibernatedAnnotation] == hibernatedStopped})
	}
	slices.SortFunc(out, func(a, b HibernatedService) int { return strings.Compare(a.Kind+"/"+a.Name, b.Kind+"/"+b.Name) })
	return out, nil
}

// ServicesToChange counts the services a schedule would stop (active false) or start now,
// for dry runs.
func (e *Engine) ServicesToChange(ctx context.Context, ns string, active bool, rules []finopsv1.HibernateRule) (int, error) {
	services, err := e.Services(ctx, ns, rules)
	n := 0
	for _, s := range services {
		if (!active && s.Running) || (active && s.StoppedByCostDeck) {
			n++
		}
	}
	return n, err
}

// stoppedAlready reports whether a resource was stopped before CostDeck touched it.
func (e *Engine) stoppedAlready(obj *unstructured.Unstructured, r finopsv1.HibernateRule) bool {
	if r.Preset == presetCNPG {
		return obj.GetAnnotations()[cnpgHibernation] == "on"
	}
	n, found := intField(obj, replicasPath(r))
	return found && n == 0
}

// setService patches a resource to stop or start it, recording what to restore.
func (e *Engine) setService(ctx context.Context, obj *unstructured.Unstructured, r finopsv1.HibernateRule, active bool) error {
	annotations := map[string]any{}
	var patch map[string]any
	if r.Preset == presetCNPG {
		if active {
			annotations[cnpgHibernation], annotations[HibernatedAnnotation] = nil, hibernatedStarting
		} else {
			if obj.GetAnnotations()[cnpgHibernation] == "on" {
				return nil
			}
			annotations[cnpgHibernation], annotations[HibernatedAnnotation] = "on", hibernatedStopped
		}
		patch = map[string]any{"metadata": map[string]any{"annotations": annotations}}
	} else {
		field := replicasPath(r)
		current, _ := intField(obj, field)
		var target int64
		if active {
			// The count recorded at stop time, as for workloads; one without a record.
			target = 1
			if v, err := strconv.ParseInt(obj.GetAnnotations()[OriginalReplicasAnnotation], 10, 64); err == nil && v > 0 {
				target = v
			}
			annotations[OriginalReplicasAnnotation], annotations[HibernatedAnnotation] = nil, hibernatedStarting
		} else {
			if current == 0 {
				return nil
			}
			annotations[OriginalReplicasAnnotation], annotations[HibernatedAnnotation] = strconv.FormatInt(current, 10), hibernatedStopped
		}
		patch = map[string]any{"metadata": map[string]any{"annotations": annotations}}
		setPath(patch, strings.Split(field, "."), target)
	}
	if err := e.patchService(ctx, obj, patch); err != nil {
		return err
	}
	log.FromContext(ctx).Info("Patched service", "kind", obj.GetKind(), "namespace", obj.GetNamespace(), "name", obj.GetName(), "active", active)
	return nil
}

// patchService applies a merge patch and refreshes obj from the answer.
func (e *Engine) patchService(ctx context.Context, obj *unstructured.Unstructured, patch map[string]any) error {
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return e.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager))
}

// serviceReady reports whether a resource has reached the state: per preset, or from
// readyField for a rule without one.
func (e *Engine) serviceReady(ctx context.Context, obj *unstructured.Unstructured, r finopsv1.HibernateRule, active bool) bool {
	switch r.Preset {
	case presetCNPG:
		// A hibernated cluster can still report itself healthy: the hibernation condition
		// decides on the way down, Ready and the ready instances on the way up.
		hibernated := condition(obj, cnpgHibernation)
		if !active {
			return hibernated == statusTrue
		}
		instances, _ := intField(obj, "spec.instances")
		readyInstances, _ := intField(obj, "status.readyInstances")
		return hibernated == "" && condition(obj, readyCondition) == statusTrue && readyInstances >= instances
	case presetZalando:
		// The operator's StatefulSet, named after the cluster, shows the running pods.
		var sts appsv1.StatefulSet
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}, &sts); err != nil {
			return !active && apierrors.IsNotFound(err)
		}
		want, _ := intField(obj, "spec.numberOfInstances")
		if !active {
			return sts.Status.ReadyReplicas == 0 && sts.Status.Replicas == 0
		}
		return int64(sts.Status.ReadyReplicas) >= want
	case presetStrimzi:
		// Strimzi reports a resource with no replicas Ready too, so the pods decide.
		var pods corev1.PodList
		if err := e.Client.List(ctx, &pods, client.InNamespace(obj.GetNamespace()), client.MatchingLabels{"strimzi.io/cluster": obj.GetName(), "strimzi.io/kind": obj.GetKind()}); err != nil {
			return false
		}
		if !active {
			return !slices.ContainsFunc(pods.Items, running)
		}
		want, _ := intField(obj, "spec.replicas")
		readyPods := 0
		for _, p := range pods.Items {
			if podReady(p) {
				readyPods++
			}
		}
		return observed(obj) && condition(obj, readyCondition) == statusTrue && int64(readyPods) >= want
	}
	if r.ReadyField == "" {
		return true
	}
	ready, _ := intField(obj, r.ReadyField)
	if !active {
		return ready == 0
	}
	want, _ := intField(obj, r.ReplicasField)
	return ready >= want
}

func running(p corev1.Pod) bool {
	return p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed
}

func podReady(p corev1.Pod) bool {
	return p.DeletionTimestamp == nil && slices.ContainsFunc(p.Status.Conditions, func(c corev1.PodCondition) bool {
		return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue
	})
}

// observed reports whether the operator has seen the latest spec, where it says so.
func observed(obj *unstructured.Unstructured) bool {
	seen, found := intField(obj, "status.observedGeneration")
	return !found || seen >= obj.GetGeneration()
}

func replicasPath(r finopsv1.HibernateRule) string {
	switch r.Preset {
	case presetZalando:
		return "spec.numberOfInstances"
	case presetStrimzi:
		return "spec.replicas"
	}
	return r.ReplicasField
}

func intField(obj *unstructured.Unstructured, dotted string) (int64, bool) {
	v, found, err := unstructured.NestedFieldNoCopy(obj.Object, strings.Split(dotted, ".")...)
	if err != nil || !found {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	}
	return 0, false
}

func condition(obj *unstructured.Unstructured, kind string) string {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == kind {
			s, _ := m["status"].(string)
			return s
		}
	}
	return ""
}

// setPath sets a value at a dotted path inside a merge patch.
func setPath(patch map[string]any, keys []string, value any) {
	m := patch
	for _, k := range keys[:len(keys)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[keys[len(keys)-1]] = value
}

// ownedByKind returns the kind of an object's controller when an owner: exclusion names
// it, or "".
func ownedByKind(obj client.Object, exclusions []string) string {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		for _, ex := range exclusions {
			if kind, ok := strings.CutPrefix(strings.TrimSpace(ex), "owner:"); ok && strings.EqualFold(kind, ref.Kind) {
				return ref.Kind
			}
		}
	}
	return ""
}

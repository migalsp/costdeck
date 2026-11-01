package scaling

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GitOps tools reconcile workloads back to what Git says, so a scale-down to zero lasts only
// until their next sync. CostDeck finds which tool manages each workload and, when a
// schedule sets spec.pauseGitOps, pauses that tool while the schedule keeps it down:
// Flux Kustomizations and HelmReleases are suspended, Argo CD Applications lose self-heal.
// It resumes only what it paused itself, marked with GitOpsPausedAnnotation.

const (
	// GitOpsPausedAnnotation marks a Kustomization, HelmRelease or Application CostDeck
	// paused; its value says what to restore.
	GitOpsPausedAnnotation = "costdeck.io/gitops-paused"

	argoTrackingAnnotation = "argocd.argoproj.io/tracking-id"
	fluxKustomizationName  = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizationNS    = "kustomize.toolkit.fluxcd.io/namespace"
	fluxHelmReleaseName    = "helm.toolkit.fluxcd.io/name"
	fluxHelmReleaseNS      = "helm.toolkit.fluxcd.io/namespace"

	pausedSuspend  = "suspend"
	pausedSelfHeal = "self-heal"
)

var (
	kustomizationGVK = schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}
	helmReleaseGVK   = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
	applicationGVK   = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}
)

// GitOpsOwner is the GitOps object that manages a workload.
type GitOpsOwner struct {
	// Kind is Application (Argo CD), Kustomization or HelmRelease (Flux).
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

func (o GitOpsOwner) String() string {
	ref := o.Name
	if o.Namespace != "" {
		ref = o.Namespace + "/" + o.Name
	}
	if o.Kind == applicationGVK.Kind {
		return "Argo CD application " + ref
	}
	return "Flux " + o.Kind + " " + ref
}

// GitOpsOwnerOf returns the GitOps object managing a workload, from the markers Argo CD
// (annotation tracking, its default since v3) and Flux put on what they apply, or nil.
func GitOpsOwnerOf(obj metav1.Object) *GitOpsOwner {
	labels := obj.GetLabels()
	if name := labels[fluxKustomizationName]; name != "" {
		return &GitOpsOwner{Kind: kustomizationGVK.Kind, Name: name, Namespace: labels[fluxKustomizationNS]}
	}
	if name := labels[fluxHelmReleaseName]; name != "" {
		return &GitOpsOwner{Kind: helmReleaseGVK.Kind, Name: name, Namespace: labels[fluxHelmReleaseNS]}
	}
	// <app-instance>:<group>/<kind>:<namespace>/<name>, where the instance is the
	// application's name, or <namespace>_<name> outside Argo CD's own namespace.
	if id := obj.GetAnnotations()[argoTrackingAnnotation]; id != "" {
		instance, _, _ := strings.Cut(id, ":")
		if ns, name, ok := strings.Cut(instance, "_"); ok {
			return &GitOpsOwner{Kind: applicationGVK.Kind, Name: name, Namespace: ns}
		}
		return &GitOpsOwner{Kind: applicationGVK.Kind, Name: instance}
	}
	return nil
}

// GitOpsOwners lists, sorted and without duplicates, the GitOps objects managing the
// workloads a schedule scales in a namespace.
func (e *Engine) GitOpsOwners(ctx context.Context, ns string, exclusions []string) ([]GitOpsOwner, error) {
	owners, _, err := e.gitOpsOwnersWithWorkloads(ctx, ns, exclusions)
	return owners, err
}

// PauseGitOps pauses the GitOps objects that would bring a namespace's workloads back while
// it is down. allowed are the namespaces the schedule scales. An object is paused only when
// it shows that it manages these workloads and nothing outside allowed, because the labels
// and annotations naming it are written by whoever can edit the workloads. It returns what
// it could not pause, and why.
func (e *Engine) PauseGitOps(ctx context.Context, ns string, exclusions, allowed []string) ([]string, error) {
	owners, managed, err := e.gitOpsOwnersWithWorkloads(ctx, ns, exclusions)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, o := range owners {
		obj, err := e.gitOpsObject(ctx, o, managed[o])
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: %s", o, gitOpsError(err)))
			continue
		}
		if obj.GetAnnotations()[GitOpsPausedAnnotation] != "" {
			continue
		}
		var patch map[string]any
		switch o.Kind {
		case kustomizationGVK.Kind, helmReleaseGVK.Kind:
			if suspended, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend"); suspended {
				continue
			}
			patch = map[string]any{"spec": map[string]any{"suspend": true}}
		case applicationGVK.Kind:
			if selfHeal, _, _ := unstructured.NestedBool(obj.Object, "spec", "syncPolicy", "automated", "selfHeal"); !selfHeal {
				continue
			}
			if set := ownerKind(obj, "ApplicationSet"); set != "" {
				notes = append(notes, fmt.Sprintf("%s comes from the ApplicationSet %s, which would undo a pause; ignore the replicas in Argo CD instead", o, set))
				continue
			}
			patch = map[string]any{"spec": map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": false}}}}
		}
		if reason := outsideTheSchedule(obj, o, managed[o], allowed); reason != "" {
			notes = append(notes, fmt.Sprintf("%s %s, so it is left running", o, reason))
			continue
		}
		value := pausedSuspend
		if o.Kind == applicationGVK.Kind {
			value = pausedSelfHeal
		}
		patch["metadata"] = map[string]any{"annotations": map[string]any{GitOpsPausedAnnotation: value}}
		if err := e.patchGitOps(ctx, obj, patch); err != nil {
			notes = append(notes, fmt.Sprintf("%s: %s", o, gitOpsError(err)))
		}
	}
	return notes, nil
}

// ResumeGitOps resumes the GitOps objects CostDeck paused for a namespace's workloads.
// Self-heal comes back only while automated sync is still on, so a sync someone switched to
// manual during the pause stays manual.
func (e *Engine) ResumeGitOps(ctx context.Context, ns string, exclusions []string) error {
	owners, managed, err := e.gitOpsOwnersWithWorkloads(ctx, ns, exclusions)
	if err != nil {
		return err
	}
	for _, o := range owners {
		obj, err := e.gitOpsObject(ctx, o, managed[o])
		if err != nil {
			continue
		}
		patch := map[string]any{}
		switch obj.GetAnnotations()[GitOpsPausedAnnotation] {
		case pausedSuspend:
			patch["spec"] = map[string]any{"suspend": false}
		case pausedSelfHeal:
			if automated, found, _ := unstructured.NestedMap(obj.Object, "spec", "syncPolicy", "automated"); found && automated != nil {
				patch["spec"] = map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}}
			}
		default:
			continue
		}
		patch["metadata"] = map[string]any{"annotations": map[string]any{GitOpsPausedAnnotation: nil}}
		if err := e.patchGitOps(ctx, obj, patch); err != nil {
			return fmt.Errorf("resume %s: %w", o, err)
		}
	}
	return nil
}

func (e *Engine) gitOpsOwnersWithWorkloads(ctx context.Context, ns string, exclusions []string) ([]GitOpsOwner, map[GitOpsOwner][]client.Object, error) {
	objs, err := e.listScalableResources(ctx, ns, exclusions)
	if err != nil {
		return nil, nil, err
	}
	managed := map[GitOpsOwner][]client.Object{}
	var owners []GitOpsOwner
	for _, obj := range objs {
		o := GitOpsOwnerOf(obj)
		if o == nil {
			continue
		}
		if _, seen := managed[*o]; !seen {
			owners = append(owners, *o)
		}
		managed[*o] = append(managed[*o], obj)
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].String() < owners[j].String() })
	return owners, managed, nil
}

// gitOpsObject reads the object behind an owner. An Application tracked without its
// namespace lives in Argo CD's own namespace; of the Applications with that name, the one
// that lists the workloads among its resources is it.
func (e *Engine) gitOpsObject(ctx context.Context, o GitOpsOwner, workloads []client.Object) (*unstructured.Unstructured, error) {
	gvk := map[string]schema.GroupVersionKind{kustomizationGVK.Kind: kustomizationGVK, helmReleaseGVK.Kind: helmReleaseGVK, applicationGVK.Kind: applicationGVK}[o.Kind]
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if o.Namespace != "" {
		return obj, e.Client.Get(ctx, types.NamespacedName{Namespace: o.Namespace, Name: o.Name}, obj)
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := e.Client.List(ctx, list); err != nil {
		return nil, err
	}
	var named, listing []*unstructured.Unstructured
	for i := range list.Items {
		if list.Items[i].GetName() != o.Name {
			continue
		}
		named = append(named, &list.Items[i])
		if len(notListedByApplication(&list.Items[i], workloads)) == 0 {
			listing = append(listing, &list.Items[i])
		}
	}
	switch {
	case len(listing) == 1:
		return listing[0], nil
	case len(listing) > 1:
		return nil, fmt.Errorf("%d Applications named %s list these workloads; it is not clear which one manages them", len(listing), o.Name)
	case len(named) == 1:
		return named[0], nil
	case len(named) == 0:
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: gvk.Group, Resource: strings.ToLower(gvk.Kind) + "s"}, o.Name)
	}
	return nil, fmt.Errorf("%d Applications are named %s and none lists these workloads", len(named), o.Name)
}

func outsideTheSchedule(obj *unstructured.Unstructured, o GitOpsOwner, workloads []client.Object, allowed []string) string {
	switch o.Kind {
	case kustomizationGVK.Kind:
		if missing := notInInventory(obj, workloads); len(missing) > 0 {
			return "does not list " + strings.Join(missing, ", ") + " in its inventory"
		}
		if outside := inventoryOutside(obj, allowed); len(outside) > 0 {
			return "also applies to " + strings.Join(outside, ", ")
		}
	case helmReleaseGVK.Kind:
		target, _, _ := unstructured.NestedString(obj.Object, "spec", "targetNamespace")
		if target == "" {
			target = obj.GetNamespace()
		}
		for _, w := range workloads {
			if w.GetNamespace() != target {
				return fmt.Sprintf("installs into %s, not %s", target, w.GetNamespace())
			}
		}
		if !slices.Contains(allowed, target) {
			return "installs into " + target + ", which the schedule does not scale"
		}
	case applicationGVK.Kind:
		if missing := notListedByApplication(obj, workloads); len(missing) > 0 {
			return "does not list " + strings.Join(missing, ", ") + " among its resources"
		}
		if outside := applicationOutside(obj, allowed); len(outside) > 0 {
			return "also manages " + strings.Join(outside, ", ")
		}
	}
	return ""
}

func namespaceObjectIn(kind, name string, allowed []string) bool {
	return kind == "Namespace" && slices.Contains(allowed, name)
}

func workloadRef(w client.Object) (group, kind string) {
	kind = workloadKind(w)
	if kind == kindRollout {
		return applicationGVK.Group, kind
	}
	return "apps", kind
}

func notInInventory(obj *unstructured.Unstructured, workloads []client.Object) []string {
	entries, _, _ := unstructured.NestedSlice(obj.Object, "status", "inventory", "entries")
	ids := map[string]bool{}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		if id, _ := m["id"].(string); id != "" {
			ids[id] = true
		}
	}
	var missing []string
	for _, w := range workloads {
		group, kind := workloadRef(w)
		if !ids[w.GetNamespace()+"_"+w.GetName()+"_"+group+"_"+kind] {
			missing = append(missing, kind+" "+w.GetName())
		}
	}
	return missing
}

func notListedByApplication(obj *unstructured.Unstructured, workloads []client.Object) []string {
	resources, _, _ := unstructured.NestedSlice(obj.Object, "status", "resources")
	listed := map[string]bool{}
	for _, r := range resources {
		m, _ := r.(map[string]any)
		group, _ := m["group"].(string)
		kind, _ := m["kind"].(string)
		ns, _ := m["namespace"].(string)
		name, _ := m["name"].(string)
		listed[group+"/"+kind+"/"+ns+"/"+name] = true
	}
	var missing []string
	for _, w := range workloads {
		group, kind := workloadRef(w)
		if !listed[group+"/"+kind+"/"+w.GetNamespace()+"/"+w.GetName()] {
			missing = append(missing, kind+" "+w.GetName())
		}
	}
	return missing
}

func applicationOutside(obj *unstructured.Unstructured, allowed []string) []string {
	var out []string
	add := func(what string) {
		if !slices.Contains(out, what) {
			out = append(out, what)
		}
	}
	if dest, _, _ := unstructured.NestedString(obj.Object, "spec", "destination", "namespace"); dest != "" && !slices.Contains(allowed, dest) {
		add(dest)
	}
	resources, _, _ := unstructured.NestedSlice(obj.Object, "status", "resources")
	for _, r := range resources {
		m, _ := r.(map[string]any)
		kind, _ := m["kind"].(string)
		ns, _ := m["namespace"].(string)
		name, _ := m["name"].(string)
		switch {
		case ns != "" && !slices.Contains(allowed, ns):
			add(ns)
		case ns == "" && !namespaceObjectIn(kind, name, allowed):
			add("cluster-scoped resources")
		}
	}
	sort.Strings(out)
	return out
}

func (e *Engine) patchGitOps(ctx context.Context, obj *unstructured.Unstructured, patch map[string]any) error {
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return e.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager))
}

// inventoryOutside lists the namespaces a Flux Kustomization applies to besides allowed,
// from its inventory (entry ids are <namespace>_<name>_<group>_<kind>, with an empty
// namespace for cluster-scoped objects).
func inventoryOutside(obj *unstructured.Unstructured, allowed []string) []string {
	entries, _, _ := unstructured.NestedSlice(obj.Object, "status", "inventory", "entries")
	var out []string
	add := func(what string) {
		if !slices.Contains(out, what) {
			out = append(out, what)
		}
	}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		id, _ := m["id"].(string)
		parts := strings.SplitN(id, "_", 4)
		if len(parts) != 4 {
			continue
		}
		ns, name, kind := parts[0], parts[1], parts[3]
		switch {
		case ns != "" && !slices.Contains(allowed, ns):
			add(ns)
		case ns == "" && !namespaceObjectIn(kind, name, allowed):
			add("cluster-scoped resources")
		}
	}
	sort.Strings(out)
	return out
}

func ownerKind(obj metav1.Object, kind string) string {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == kind {
			return ref.Name
		}
	}
	return ""
}

// gitOpsError explains why a GitOps object could not be read or paused.
func gitOpsError(err error) string {
	switch {
	case apierrors.IsForbidden(err):
		return "no permission to pause it; enable gitops.pause in the Helm chart"
	case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
		return "not found"
	}
	return err.Error()
}

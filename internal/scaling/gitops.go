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
	objs, err := e.listScalableResources(ctx, ns, exclusions)
	if err != nil {
		return nil, err
	}
	var out []GitOpsOwner
	for _, obj := range objs {
		if o := GitOpsOwnerOf(obj); o != nil && !slices.Contains(out, *o) {
			out = append(out, *o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// PauseGitOps pauses the GitOps objects that would bring a namespace's workloads back while
// it is down. allowed are the namespaces the schedule scales: a Kustomization that also
// applies to others is left running. It returns what it could not pause, and why.
func (e *Engine) PauseGitOps(ctx context.Context, ns string, exclusions, allowed []string) ([]string, error) {
	owners, err := e.GitOpsOwners(ctx, ns, exclusions)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, o := range owners {
		obj, err := e.gitOpsObject(ctx, o)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: %s", o, gitOpsError(err)))
			continue
		}
		if obj.GetAnnotations()[GitOpsPausedAnnotation] != "" {
			continue // paused already
		}
		var patch map[string]any
		switch o.Kind {
		case kustomizationGVK.Kind, helmReleaseGVK.Kind:
			if suspended, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend"); suspended {
				continue // suspended by someone else: theirs to resume
			}
			if outside := inventoryOutside(obj, allowed); len(outside) > 0 {
				notes = append(notes, fmt.Sprintf("%s also applies to %s, so it is left running", o, strings.Join(outside, ", ")))
				continue
			}
			patch = map[string]any{"spec": map[string]any{"suspend": true}}
		case applicationGVK.Kind:
			if set := ownerKind(obj, "ApplicationSet"); set != "" {
				notes = append(notes, fmt.Sprintf("%s comes from the ApplicationSet %s, which would undo a pause; ignore the replicas in Argo CD instead", o, set))
				continue
			}
			if selfHeal, _, _ := unstructured.NestedBool(obj.Object, "spec", "syncPolicy", "automated", "selfHeal"); !selfHeal {
				continue // nothing would undo the scale-down
			}
			patch = map[string]any{"spec": map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": false}}}}
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
func (e *Engine) ResumeGitOps(ctx context.Context, ns string, exclusions []string) error {
	owners, err := e.GitOpsOwners(ctx, ns, exclusions)
	if err != nil {
		return err
	}
	for _, o := range owners {
		obj, err := e.gitOpsObject(ctx, o)
		if err != nil {
			continue // nothing CostDeck could have paused
		}
		var patch map[string]any
		switch obj.GetAnnotations()[GitOpsPausedAnnotation] {
		case pausedSuspend:
			patch = map[string]any{"spec": map[string]any{"suspend": false}}
		case pausedSelfHeal:
			patch = map[string]any{"spec": map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}}}
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

// gitOpsObject reads the object behind an owner. An Application tracked without its
// namespace lives in Argo CD's namespace, which is found by name.
func (e *Engine) gitOpsObject(ctx context.Context, o GitOpsOwner) (*unstructured.Unstructured, error) {
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
	for i := range list.Items {
		if list.Items[i].GetName() == o.Name {
			return &list.Items[i], nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: gvk.Group, Resource: strings.ToLower(gvk.Kind) + "s"}, o.Name)
}

func (e *Engine) patchGitOps(ctx context.Context, obj *unstructured.Unstructured, patch map[string]any) error {
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return e.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager))
}

// inventoryOutside lists the namespaces a Flux Kustomization applies to besides allowed,
// from its inventory (entry ids are <namespace>_<name>_<group>_<kind>).
func inventoryOutside(obj *unstructured.Unstructured, allowed []string) []string {
	entries, _, _ := unstructured.NestedSlice(obj.Object, "status", "inventory", "entries")
	var out []string
	for _, e := range entries {
		m, _ := e.(map[string]any)
		id, _ := m["id"].(string)
		ns, _, _ := strings.Cut(id, "_")
		if ns != "" && !slices.Contains(allowed, ns) && !slices.Contains(out, ns) {
			out = append(out, ns)
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

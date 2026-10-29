// Package estimate prices the change between two sets of rendered Kubernetes manifests,
// such as the base branch and a pull request run through helm template or kustomize
// build, so a pull request can say what it will cost before it is merged.
package estimate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// Prices is what the estimate prices with.
type Prices struct {
	Rates pricing.Rates
	// Storage prices a GiB-month of a StorageClass ("" is the default class); nil prices
	// volumes at nothing.
	Storage func(class string) float64
	// LoadBalancerMonth is one load balancer's monthly price.
	LoadBalancerMonth float64
	// Nodes is how many nodes a DaemonSet runs on; zero counts one.
	Nodes int
	// UpShare is the share of the month a namespace's workloads run (its schedule) and a
	// note saying so; nil means always.
	UpShare func(namespace string) (float64, string)
}

// Usage is one side of a change.
type Usage struct {
	Replicas   int32   `json:"replicas"`
	CPU        string  `json:"cpu"`
	Memory     string  `json:"memory"`
	GPU        float64 `json:"gpu,omitempty"`
	StorageGiB float64 `json:"storageGiB,omitempty"`
	Monthly    float64 `json:"monthly"`
}

// Item is one object whose cost changes.
type Item struct {
	Kind      string  `json:"kind"`
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	Before    *Usage  `json:"before,omitempty"`
	After     *Usage  `json:"after,omitempty"`
	Delta     float64 `json:"delta"`
	Note      string  `json:"note,omitempty"`
}

// Result is the priced change.
type Result struct {
	Currency  string   `json:"currency"`
	Basis     string   `json:"basis"`
	Items     []Item   `json:"items"`
	Before    float64  `json:"before"`
	After     float64  `json:"after"`
	Delta     float64  `json:"delta"`
	Unchanged int      `json:"unchanged"`
	Warnings  []string `json:"warnings"`
}

// object is a priced object of one side.
type object struct {
	kind, namespace, name string
	usage                 Usage
	note                  string
	negative              bool
}

func (o object) key() string { return o.kind + "/" + o.namespace + "/" + o.name }

// Estimate prices both sides and lists what changes. Objects without a namespace are in
// defaultNamespace.
func Estimate(base, head []byte, defaultNamespace string, p Prices) (Result, error) {
	r := Result{Currency: p.Rates.Currency, Basis: p.Rates.Basis, Items: []Item{}, Warnings: []string{}}
	// Only the head's warnings matter: they describe what is about to run.
	before, _, err := price(base, defaultNamespace, p)
	if err != nil {
		return r, fmt.Errorf("base: %w", err)
	}
	after, warnAfter, err := price(head, defaultNamespace, p)
	if err != nil {
		return r, fmt.Errorf("head: %w", err)
	}
	r.Warnings = append(r.Warnings, warnAfter...)
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	for k := range keys {
		b, inBefore := before[k]
		a, inAfter := after[k]
		r.Before += b.usage.Monthly
		r.After += a.usage.Monthly
		if inBefore && inAfter && math.Abs(a.usage.Monthly-b.usage.Monthly) < 0.005 && a.usage == b.usage {
			r.Unchanged++
			continue
		}
		it := Item{Kind: a.kind, Namespace: a.namespace, Name: a.name, Note: a.note, Delta: round(a.usage.Monthly - b.usage.Monthly)}
		if !inAfter {
			it.Kind, it.Namespace, it.Name, it.Note = b.kind, b.namespace, b.name, b.note
		}
		if inBefore {
			u := b.usage
			it.Before = &u
		}
		if inAfter {
			u := a.usage
			it.After = &u
		}
		r.Items = append(r.Items, it)
	}
	sort.Slice(r.Items, func(i, j int) bool {
		if math.Abs(r.Items[i].Delta) != math.Abs(r.Items[j].Delta) {
			return math.Abs(r.Items[i].Delta) > math.Abs(r.Items[j].Delta)
		}
		return r.Items[i].Kind+r.Items[i].Namespace+r.Items[i].Name < r.Items[j].Kind+r.Items[j].Namespace+r.Items[j].Name
	})
	r.Before, r.After = round(r.Before), round(r.After)
	r.Delta = round(r.After - r.Before)
	return r, nil
}

// decode splits multi-document YAML or JSON into objects, unpacking Lists.
func decode(raw []byte) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		if len(m) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: m}
		if strings.HasSuffix(u.GetKind(), "List") {
			items, _, _ := unstructured.NestedSlice(m, "items")
			for _, it := range items {
				if im, ok := it.(map[string]any); ok {
					out = append(out, &unstructured.Unstructured{Object: im})
				}
			}
			continue
		}
		out = append(out, u)
	}
}

// price prices the objects of one side by kind/namespace/name.
func price(raw []byte, defaultNamespace string, p Prices) (map[string]object, []string, error) {
	objs, err := decode(raw)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	// HPAs raise their target's replicas to at least minReplicas.
	floors := map[string]int32{}
	for _, u := range objs {
		if u.GetKind() != "HorizontalPodAutoscaler" {
			continue
		}
		var hpa autoscalingv2.HorizontalPodAutoscaler
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &hpa) != nil {
			continue
		}
		ns := namespaceOf(u, defaultNamespace)
		minReplicas := int32(1)
		if hpa.Spec.MinReplicas != nil {
			minReplicas = *hpa.Spec.MinReplicas
		}
		floors[hpa.Spec.ScaleTargetRef.Kind+"/"+ns+"/"+hpa.Spec.ScaleTargetRef.Name] = minReplicas
		if hpa.Spec.MaxReplicas > minReplicas {
			warnings = append(warnings, fmt.Sprintf("%s/%s scales between %d and %d replicas: the estimate counts %d.", ns, hpa.Spec.ScaleTargetRef.Name, minReplicas, hpa.Spec.MaxReplicas, minReplicas))
		}
	}
	out := map[string]object{}
	for _, u := range objs {
		o, ok := priceObject(u, namespaceOf(u, defaultNamespace), p, floors)
		if !ok {
			continue
		}
		out[o.key()] = o
		if o.negative {
			warnings = append(warnings, fmt.Sprintf("%s %s/%s asks for negative replicas, resources or storage, which Kubernetes rejects: the estimate counts them as zero.", o.kind, o.namespace, o.name))
		}
	}
	return out, warnings, nil
}

func namespaceOf(u *unstructured.Unstructured, def string) string {
	if ns := u.GetNamespace(); ns != "" {
		return ns
	}
	return def
}

// priceObject prices one object, if it costs anything.
func priceObject(u *unstructured.Unstructured, ns string, p Prices, floors map[string]int32) (object, bool) {
	o := object{kind: u.GetKind(), namespace: ns, name: u.GetName()}
	var tmpl corev1.PodTemplateSpec
	replicas := int32(1)
	var claims []corev1.PersistentVolumeClaim
	switch o.kind {
	case "Deployment":
		var d appsv1.Deployment
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &d) != nil {
			return o, false
		}
		tmpl, replicas = d.Spec.Template, replicasOf(d.Spec.Replicas)
	case "StatefulSet":
		var s appsv1.StatefulSet
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &s) != nil {
			return o, false
		}
		tmpl, replicas, claims = s.Spec.Template, replicasOf(s.Spec.Replicas), s.Spec.VolumeClaimTemplates
	case "Rollout":
		raw, _, _ := unstructured.NestedMap(u.Object, "spec", "template")
		_ = runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &tmpl)
		if n, found, _ := unstructured.NestedInt64(u.Object, "spec", "replicas"); found {
			replicas = int32(max(min(n, math.MaxInt32), math.MinInt32))
		}
	case "DaemonSet":
		var d appsv1.DaemonSet
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &d) != nil {
			return o, false
		}
		tmpl, replicas = d.Spec.Template, int32(max(p.Nodes, 1))
		o.note = fmt.Sprintf("one pod on each of %d nodes", replicas)
	case "Pod":
		var pod corev1.Pod
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &pod) != nil {
			return o, false
		}
		tmpl.Spec = pod.Spec
	case "PersistentVolumeClaim":
		var c corev1.PersistentVolumeClaim
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &c) != nil {
			return o, false
		}
		gib := gibOf(c.Spec.Resources.Requests.Storage())
		if gib < 0 {
			gib, o.negative = 0, true
		}
		o.usage = Usage{StorageGiB: gib, Monthly: round(gib * p.storage(className(c.Spec.StorageClassName)))}
		return o, true
	case "Service":
		if t, _, _ := unstructured.NestedString(u.Object, "spec", "type"); t == string(corev1.ServiceTypeLoadBalancer) {
			o.usage = Usage{Monthly: round(p.LoadBalancerMonth)}
			o.note = "a cloud load balancer"
			return o, true
		}
		return o, false
	default:
		return o, false
	}
	if replicas < 0 {
		replicas, o.negative = 0, true
	}
	if floor, ok := floors[o.kind+"/"+ns+"/"+o.name]; ok && floor > replicas {
		replicas = floor
	}
	cpu, mem := podRequests(tmpl.Spec)
	gpu := podGPUs(tmpl.Spec)
	o.negative = o.negative || hasNegativeQuantity(tmpl.Spec)
	share, note := 1.0, ""
	if p.UpShare != nil {
		share, note = p.UpShare(ns)
	}
	if note != "" {
		o.note = strings.TrimPrefix(strings.Join([]string{o.note, note}, "; "), "; ")
	}
	pod := p.Rates.Monthly(cpu, mem) + gpu*p.Rates.GPUHour*pricing.HoursPerMonth
	compute := pod * float64(replicas) * share
	var storage float64
	for _, c := range claims {
		perPod := gibOf(c.Spec.Resources.Requests.Storage())
		if perPod < 0 {
			perPod, o.negative = 0, true
		}
		gib := perPod * float64(replicas)
		o.usage.StorageGiB += gib
		storage += gib * p.storage(className(c.Spec.StorageClassName))
	}
	o.usage.Replicas, o.usage.GPU = replicas, gpu*float64(replicas)
	o.usage.CPU, o.usage.Memory = quantity(cpu, replicas), quantity(mem, replicas)
	o.usage.Monthly = round(compute + storage)
	return o, true
}

func (p Prices) storage(class string) float64 {
	if p.Storage == nil {
		return 0
	}
	return p.Storage(class)
}

func className(c *string) string {
	if c == nil {
		return ""
	}
	return *c
}

func replicasOf(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

// podRequests sums a pod's container requests, or its largest init container when that is
// bigger, as the scheduler counts them.
func podRequests(spec corev1.PodSpec) (cpu, mem resource.Quantity) {
	for _, c := range spec.Containers {
		for _, pair := range []struct{ total, q *resource.Quantity }{{&cpu, c.Resources.Requests.Cpu()}, {&mem, c.Resources.Requests.Memory()}} {
			if pair.q.Sign() > 0 {
				pair.total.Add(*pair.q)
			}
		}
	}
	for _, c := range spec.InitContainers {
		for _, pair := range []struct{ total, q *resource.Quantity }{{&cpu, c.Resources.Requests.Cpu()}, {&mem, c.Resources.Requests.Memory()}} {
			if pair.q.Cmp(*pair.total) > 0 {
				*pair.total = pair.q.DeepCopy()
			}
		}
	}
	return cpu, mem
}

func hasNegativeQuantity(spec corev1.PodSpec) bool {
	for _, c := range slices.Concat(spec.InitContainers, spec.Containers) {
		for _, list := range []corev1.ResourceList{c.Resources.Requests, c.Resources.Limits} {
			for _, q := range list {
				if q.Sign() < 0 {
					return true
				}
			}
		}
	}
	return false
}
// podGPUs counts the GPUs a pod's containers ask for, by limit as the device plugins do.
func podGPUs(spec corev1.PodSpec) float64 {
	var n float64
	for _, c := range spec.Containers {
		for name, q := range c.Resources.Limits {
			if strings.HasSuffix(string(name), "/gpu") && q.Sign() > 0 {
				n += q.AsApproximateFloat64()
			}
		}
	}
	return n
}

func gibOf(q *resource.Quantity) float64 { return q.AsApproximateFloat64() / (1 << 30) }

// quantity is a per-pod quantity times the replicas, as kubectl writes it.
func quantity(q resource.Quantity, replicas int32) string {
	total := q.DeepCopy()
	total.Mul(int64(replicas))
	return total.String()
}

func round(v float64) float64 { return math.Round(v*100) / 100 }

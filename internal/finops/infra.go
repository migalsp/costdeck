package finops

import (
	"context"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	storagev1 "k8s.io/api/storage/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// Volumes and load balancers are the classic waste the node view cannot see: disks that
// outlived their pods, and load balancers still billed while nothing answers behind them.

// Read-only access to price volumes and load balancers.
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims;persistentvolumes;services,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch

const (
	defaultClassAnnotation = "storageclass.kubernetes.io/is-default-class"
	annotationTrue         = "true"
)

// Volume is one persistent volume: a claim, or a released volume with none.
type Volume struct {
	Namespace    string   `json:"namespace,omitempty"`
	Claim        string   `json:"claim,omitempty"`
	Volume       string   `json:"volume,omitempty"`
	StorageClass string   `json:"storageClass,omitempty"`
	DiskType     string   `json:"diskType,omitempty"`
	Phase        string   `json:"phase"`
	SizeGiB      float64  `json:"sizeGiB"`
	MonthlyCost  float64  `json:"monthlyCost"`
	Local        bool     `json:"local,omitempty"`
	MountedBy    []string `json:"mountedBy"`
	// State is in-use, unused (bound but mounted by no pod), scaled-down (unmounted while
	// the namespace's schedule keeps it down, as expected) or orphaned (a released volume
	// no claim uses, still billed).
	State string `json:"state"`
	Basis string `json:"basis"`
}

// LoadBalancer is one Service of type LoadBalancer.
type LoadBalancer struct {
	Namespace      string   `json:"namespace"`
	Name           string   `json:"name"`
	Address        string   `json:"address,omitempty"`
	Ports          []string `json:"ports"`
	ReadyEndpoints int      `json:"readyEndpoints"`
	MonthlyCost    float64  `json:"monthlyCost"`
	// State is serving, idle (no ready endpoint) or scaled-down (its namespace is kept down
	// by a schedule while the load balancer is still billed).
	State string `json:"state"`
	Basis string `json:"basis"`
}

// Volume and load balancer states.
const (
	StateInUse      = "in-use"
	StateUnused     = "unused"
	StateScaledDown = "scaled-down"
	StateOrphaned   = "orphaned"
	StateServing    = "serving"
	StateIdle       = "idle"
)

// Infra is the storage and network side of the cluster.
type Infra struct {
	Volumes        []Volume       `json:"volumes"`
	LoadBalancers  []LoadBalancer `json:"loadBalancers"`
	StorageMonthly float64        `json:"storageMonthly"`
	NetworkMonthly float64        `json:"networkMonthly"`
	// Unused sums volumes nobody mounts and orphaned volumes; Idle sums load balancers
	// with nothing behind them.
	UnusedStorageMonthly float64 `json:"unusedStorageMonthly"`
	IdleNetworkMonthly   float64 `json:"idleNetworkMonthly"`
	NetworkBasis         string  `json:"networkBasis"`
}

// buildInfra lists claims, volumes, classes and LoadBalancer Services. live reads
// EndpointSlices without a cache, so no cluster-wide informer is started for them. down
// names the namespaces a schedule keeps down right now.
func buildInfra(ctx context.Context, c, live client.Reader, cloud string, custom pricing.InfraRates, pods []corev1.Pod, down map[string]bool) (Infra, error) {
	inf := Infra{Volumes: []Volume{}, LoadBalancers: []LoadBalancer{}}
	pr, err := newVolumePricer(ctx, c, cloud, custom)
	if err != nil {
		return inf, err
	}
	if inf.Volumes, err = listVolumes(ctx, c, pr, mountsOf(pods), down); err != nil {
		return inf, err
	}
	if inf.LoadBalancers, inf.NetworkBasis, err = listLoadBalancers(ctx, c, live, cloud, custom, down); err != nil {
		return inf, err
	}
	for _, v := range inf.Volumes {
		inf.StorageMonthly += v.MonthlyCost
		if v.State == StateUnused || v.State == StateOrphaned {
			inf.UnusedStorageMonthly += v.MonthlyCost
		}
	}
	for _, lb := range inf.LoadBalancers {
		inf.NetworkMonthly += lb.MonthlyCost
		if lb.State != StateServing {
			inf.IdleNetworkMonthly += lb.MonthlyCost
		}
	}
	return inf, nil
}

// volumePricer prices volumes by storage class.
type volumePricer struct {
	cloud        string
	custom       pricing.InfraRates
	classes      map[string]pricing.StorageClassInfo
	defaultClass string
}

func newVolumePricer(ctx context.Context, c client.Reader, cloud string, custom pricing.InfraRates) (*volumePricer, error) {
	var classes storagev1.StorageClassList
	if err := c.List(ctx, &classes); err != nil {
		return nil, err
	}
	pr := &volumePricer{cloud: cloud, custom: custom, classes: map[string]pricing.StorageClassInfo{}}
	for _, sc := range classes.Items {
		pr.classes[sc.Name] = pricing.StorageClassInfo{Provisioner: sc.Provisioner, Parameters: sc.Parameters}
		if sc.Annotations[defaultClassAnnotation] == annotationTrue {
			pr.defaultClass = sc.Name
		}
	}
	return pr, nil
}

// price returns the price basis of a class and the monthly cost of sizeGiB on it. An
// unknown class is priced as the cloud's default disk.
func (pr *volumePricer) price(class string, sizeGiB float64) (pricing.VolumePrice, float64) {
	vp := pricing.PriceVolume(pr.cloud, pr.classes[class], pr.custom)
	return vp, vp.PerGiBMonth * sizeGiB
}

// mountsOf maps namespace/claim to the active pods that mount it.
func mountsOf(pods []corev1.Pod) map[string][]string {
	mounts := map[string][]string{}
	for i := range pods {
		p := &pods[i]
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				key := p.Namespace + "/" + v.PersistentVolumeClaim.ClaimName
				mounts[key] = append(mounts[key], p.Name)
			}
		}
	}
	return mounts
}

// listVolumes prices every claim, and every released volume no claim uses any more.
func listVolumes(ctx context.Context, c client.Reader, pr *volumePricer, mounts map[string][]string, down map[string]bool) ([]Volume, error) {
	var claims corev1.PersistentVolumeClaimList
	if err := c.List(ctx, &claims); err != nil {
		return nil, err
	}
	var volumes corev1.PersistentVolumeList
	if err := c.List(ctx, &volumes); err != nil {
		return nil, err
	}
	pvByName := map[string]*corev1.PersistentVolume{}
	for i := range volumes.Items {
		pvByName[volumes.Items[i].Name] = &volumes.Items[i]
	}
	out := []Volume{}
	for i := range claims.Items {
		out = append(out, claimVolume(&claims.Items[i], pvByName[claims.Items[i].Spec.VolumeName], pr, mounts, down))
	}
	for _, pv := range volumes.Items {
		if pv.Status.Phase != corev1.VolumeReleased && pv.Status.Phase != corev1.VolumeAvailable && pv.Status.Phase != corev1.VolumeFailed {
			continue
		}
		size := pv.Spec.Capacity.Storage().AsApproximateFloat64() / gib
		vp, cost := pr.price(pv.Spec.StorageClassName, size)
		if vp.Local {
			continue // a pre-created local volume is part of its node
		}
		out = append(out, Volume{
			Volume: pv.Name, StorageClass: pv.Spec.StorageClassName, DiskType: vp.Type, Phase: string(pv.Status.Phase),
			SizeGiB: size, MonthlyCost: cost, MountedBy: []string{}, State: StateOrphaned, Basis: vp.Basis,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MonthlyCost > out[j].MonthlyCost })
	return out, nil
}

func claimVolume(pvc *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume, pr *volumePricer, mounts map[string][]string, down map[string]bool) Volume {
	class := ""
	if pvc.Spec.StorageClassName != nil {
		class = *pvc.Spec.StorageClassName
	}
	size := pvc.Spec.Resources.Requests.Storage().AsApproximateFloat64()
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		size = q.AsApproximateFloat64()
	}
	if pv != nil {
		if class == "" {
			class = pv.Spec.StorageClassName
		}
		size = pv.Spec.Capacity.Storage().AsApproximateFloat64()
	}
	if class == "" {
		class = pr.defaultClass
	}
	vp, cost := pr.price(class, size/gib)
	v := Volume{
		Namespace: pvc.Namespace, Claim: pvc.Name, Volume: pvc.Spec.VolumeName, StorageClass: class,
		DiskType: vp.Type, Phase: string(pvc.Status.Phase), SizeGiB: size / gib, MonthlyCost: cost, Local: vp.Local,
		MountedBy: mounts[pvc.Namespace+"/"+pvc.Name], Basis: vp.Basis,
	}
	if v.MountedBy == nil {
		v.MountedBy = []string{}
	}
	switch {
	case len(v.MountedBy) > 0:
		v.State = StateInUse
	case down[pvc.Namespace]:
		v.State = StateScaledDown
	case pvc.Status.Phase == corev1.ClaimBound:
		v.State = StateUnused
	default:
		v.State = StateInUse // pending claims cost nothing yet
	}
	return v
}

// listLoadBalancers prices every LoadBalancer Service and counts its ready endpoints.
func listLoadBalancers(ctx context.Context, c, live client.Reader, cloud string, custom pricing.InfraRates, down map[string]bool) ([]LoadBalancer, string, error) {
	price, basis := pricing.PriceLoadBalancer(cloud, custom)
	var services corev1.ServiceList
	if err := c.List(ctx, &services); err != nil {
		return nil, basis, err
	}
	out := []LoadBalancer{}
	for _, svc := range services.Items {
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		lb := LoadBalancer{Namespace: svc.Namespace, Name: svc.Name, MonthlyCost: price, Ports: []string{}, Basis: basis}
		for _, in := range svc.Status.LoadBalancer.Ingress {
			if in.Hostname != "" {
				lb.Address = in.Hostname
			} else if in.IP != "" {
				lb.Address = in.IP
			}
		}
		for _, p := range svc.Spec.Ports {
			lb.Ports = append(lb.Ports, portString(p))
		}
		lb.ReadyEndpoints = readyEndpoints(ctx, live, svc.Namespace, svc.Name)
		switch {
		case lb.ReadyEndpoints > 0:
			lb.State = StateServing
		case down[svc.Namespace]:
			lb.State = StateScaledDown
		default:
			lb.State = StateIdle
		}
		out = append(out, lb)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, basis, nil
}

func readyEndpoints(ctx context.Context, live client.Reader, namespace, service string) int {
	var slices discoveryv1.EndpointSliceList
	if err := live.List(ctx, &slices, client.InNamespace(namespace), client.MatchingLabels{discoveryv1.LabelServiceName: service}); err != nil {
		return 0
	}
	n := 0
	for _, s := range slices.Items {
		for _, e := range s.Endpoints {
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				n++
			}
		}
	}
	return n
}

func portString(p corev1.ServicePort) string {
	proto := string(p.Protocol)
	if proto == "" {
		proto = "TCP"
	}
	return proto + "/" + strconv.Itoa(int(p.Port))
}

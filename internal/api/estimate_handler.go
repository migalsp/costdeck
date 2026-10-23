package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/estimate"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// estimateRequest carries the rendered manifests of the base branch and of a pull request.
type estimateRequest struct {
	Base string `json:"base"`
	Head string `json:"head"`
	// Namespace is where objects without one go; defaults to "default".
	Namespace string `json:"namespace,omitempty"`
}

type estimateResponse struct {
	estimate.Result
	Markdown string `json:"markdown"`
}

// maxEstimateBytes bounds the manifests of one request.
const maxEstimateBytes = 16 << 20

// handleEstimate prices the change between two sets of manifests at this cluster's rates,
// with its schedules: what a pull request would cost before it is merged.
func (s *Server) handleEstimate(w http.ResponseWriter, r *http.Request) {
	var req estimateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEstimateBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "the body is {\"base\": manifests, \"head\": manifests}: "+err.Error())
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}
	prices, err := s.estimatePrices(r.Context(), time.Now())
	if err != nil {
		writeK8sError(w, err)
		return
	}
	res, err := estimate.Estimate([]byte(req.Base), []byte(req.Head), req.Namespace, prices)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, estimateResponse{Result: res, Markdown: res.Markdown()})
}

// estimatePrices are this cluster's: its rates, storage classes, load balancer price, node
// count and schedules.
func (s *Server) estimatePrices(ctx context.Context, now time.Time) (estimate.Prices, error) {
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		return estimate.Prices{}, err
	}
	var nodes corev1.NodeList
	if err := s.Client.List(ctx, &nodes); err != nil {
		return estimate.Prices{}, err
	}
	cloud := pricing.DetectCloud(nodes.Items)
	rates := s.costRates(ctx)
	custom := pricing.InfraRates{StorageGiBMonth: cfg.Spec.Pricing.StorageGiBMonth, LoadBalancerMonth: cfg.Spec.Pricing.LoadBalancerMonth, Currency: rates.Currency}
	classes := map[string]pricing.StorageClassInfo{}
	defaultClass := ""
	var scs storagev1.StorageClassList
	if err := s.Client.List(ctx, &scs); err == nil {
		for _, sc := range scs.Items {
			classes[sc.Name] = pricing.StorageClassInfo{Provisioner: sc.Provisioner, Parameters: sc.Parameters}
			if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
				defaultClass = sc.Name
			}
		}
	}
	lb, _ := pricing.PriceLoadBalancer(cloud, custom)
	shares, err := s.scheduleShares(ctx, now)
	if err != nil {
		return estimate.Prices{}, err
	}
	return estimate.Prices{
		Rates: rates, LoadBalancerMonth: lb, Nodes: len(nodes.Items),
		Storage: func(class string) float64 {
			if class == "" {
				class = defaultClass
			}
			return pricing.PriceVolume(cloud, classes[class], custom).PerGiBMonth
		},
		UpShare: func(ns string) (float64, string) {
			if sh, ok := shares[ns]; ok {
				return sh.share, sh.note
			}
			return 1, ""
		},
	}, nil
}

type scheduleShare struct {
	share float64
	note  string
}

// scheduleShares is the share of the next four weeks each scheduled namespace is up.
func (s *Server) scheduleShares(ctx context.Context, now time.Time) (map[string]scheduleShare, error) {
	out := map[string]scheduleShare{}
	calendars := s.calendars(ctx)
	add := func(ns, kind, name string, sp finopsv1.ScalingGroupSpec) {
		if _, done := out[ns]; done {
			return
		}
		if sp.Activation == finopsv1.ActivationOnDemand {
			out[ns] = scheduleShare{0, fmt.Sprintf("only runs while another schedule needs it (%s %s)", kind, name)}
			return
		}
		exceptions, _ := scaling.ResolveExceptions(sp.Exceptions, sp.Calendars, calendars, scaling.ExceptionZone(sp.Schedules))
		tt := scaling.Timetable{Schedules: sp.Schedules, Exceptions: exceptions}
		if len(sp.Schedules) == 0 && len(exceptions) == 0 {
			return
		}
		share := tt.ActiveShare(now, now.AddDate(0, 0, 28))
		out[ns] = scheduleShare{share, fmt.Sprintf("up %.0f%% of the time on %s %s", share*100, kind, name)}
	}
	var groups finopsv1.ScalingGroupList
	if err := s.Client.List(ctx, &groups, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, err
	}
	// The older group owns a namespace two groups claim.
	sort.Slice(groups.Items, func(i, j int) bool {
		return groups.Items[i].CreationTimestamp.Before(&groups.Items[j].CreationTimestamp)
	})
	for _, g := range groups.Items {
		for _, ns := range g.Members() {
			add(ns, "schedule", g.Name, g.Spec)
		}
	}
	var configs finopsv1.ScalingConfigList
	if err := s.Client.List(ctx, &configs, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, err
	}
	for _, c := range configs.Items {
		if c.Spec.TargetNamespace == "" {
			continue
		}
		add(c.Spec.TargetNamespace, "schedule", c.Name, finopsv1.ScalingGroupSpec{Schedules: c.Spec.Schedules, Exceptions: c.Spec.Exceptions, Calendars: c.Spec.Calendars})
	}
	return out, nil
}

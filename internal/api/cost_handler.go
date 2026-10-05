package api

import (
	"context"
	"net/http"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// CostResponse describes the cost calculated
type CostResponse struct {
	HourlyCost   float64 `json:"hourlyCost"`
	MonthlyCost  float64 `json:"monthlyCost"`
	Currency     string  `json:"currency"`
	DeterminedBy string  `json:"determinedBy"`
}

// CostRequest receives target details
type CostRequest struct {
	TargetType   string  `json:"targetType"` // "namespace" or "node" or "cluster"
	TargetName   string  `json:"targetName"`
	TotalCPU     float64 `json:"totalCpu"` // Extracted in UI or backend
	TotalMemory  float64 `json:"totalMemoryGb"`
	ProviderType string  `json:"providerType,omitempty"`
}

// pricingResolver returns the server's rate resolver, building one on first use.
func (s *Server) pricingResolver() *pricing.Resolver {
	s.pricingOnce.Do(func() {
		if s.Pricing == nil {
			s.Pricing = &pricing.Resolver{Client: s.Client, AWS: awsPricerFromConfig(s.Client)}
		}
	})
	return s.Pricing
}

// costRates returns the rates every cost estimate uses.
func (s *Server) costRates(ctx context.Context) pricing.Rates {
	return s.pricingResolver().Rates(ctx)
}

// awsPricerFromConfig builds AWS Price List clients with the credentials configured for
// the AWS provider, or the pod identity when none are stored.
func awsPricerFromConfig(c client.Reader) func(context.Context, *finopsv1.CostDeckConfig) (pricing.NodePricer, error) {
	var mu sync.Mutex
	var cached *pricing.AWSPricer
	var cachedKey string
	return func(ctx context.Context, cfg *finopsv1.CostDeckConfig) (pricing.NodePricer, error) {
		secretRef, region := "", ""
		if a := cfg.Spec.Providers.AWS; a != nil {
			secretRef, region = a.SecretRef, a.Region
		}
		key := secretRef + "|" + region
		mu.Lock()
		defer mu.Unlock()
		if cached != nil && cachedKey == key {
			return cached, nil
		}
		var prov *scaling.AWSProvider
		var err error
		if secretRef != "" {
			prov, err = scaling.NewAWSProviderFromSecret(ctx, c, secretRef, cfg.Namespace, region)
		} else {
			prov, err = scaling.NewAWSProvider(ctx)
		}
		if err != nil {
			return nil, err
		}
		cached, cachedKey = pricing.NewAWSPricer(prov.Config()), key
		return cached, nil
	}
}

func (s *Server) handleCosting(w http.ResponseWriter, r *http.Request) {
	var req CostRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	ctx := r.Context()
	rates := s.costRates(ctx)

	var cpu, mem resource.Quantity
	switch req.TargetType {
	case "cluster", "node":
		cpu = *resource.NewMilliQuantity(int64(req.TotalCPU*1000), resource.DecimalSI)
		mem = *resource.NewQuantity(int64(req.TotalMemory*(1<<30)), resource.BinarySI)
	case "namespace":
		var pods corev1.PodList
		if err := s.Client.List(ctx, &pods, client.InNamespace(req.TargetName)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				continue
			}
			c, m := calculatePodRequests(p)
			cpu.Add(*c)
			mem.Add(*m)
		}
	default:
		writeError(w, http.StatusBadRequest, "targetType must be cluster, node or namespace")
		return
	}

	hourly := rates.Hourly(cpu, mem)
	writeJSON(w, http.StatusOK, CostResponse{
		HourlyCost:   hourly,
		MonthlyCost:  hourly * pricing.HoursPerMonth,
		Currency:     rates.Currency,
		DeterminedBy: rates.Basis,
	})
}

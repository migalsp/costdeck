package api

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/pricing"
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
			s.Pricing = &pricing.Resolver{Client: s.Client, AWS: pricing.AWSFromConfig(s.Client), Azure: pricing.AzureRetail()}
		}
	})
	return s.Pricing
}

// costRates returns the rates every cost estimate uses.
func (s *Server) costRates(ctx context.Context) pricing.Rates {
	return s.pricingResolver().Rates(ctx)
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

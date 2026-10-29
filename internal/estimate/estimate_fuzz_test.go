package estimate

import (
	"math"
	"testing"
)

func FuzzEstimate(f *testing.F) {
	f.Add([]byte(base), []byte(head))
	f.Add([]byte(""), []byte(deployment("shop", "api", 3)))
	f.Add([]byte("apiVersion: v1\nkind: List\nitems: [{}]"), []byte("{}"))
	f.Add([]byte("---\n---\n"), []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p"},"spec":{"containers":[{"name":"c","resources":{"requests":{"cpu":"1e3","memory":"9Ei"}}}]}}`))
	f.Add([]byte("kind: Deployment\nspec: {replicas: -5}"), []byte("apiVersion: apps/v1\nkind: StatefulSet\nspec:\n  volumeClaimTemplates: [{spec: {resources: {requests: {storage: 1Pi}}}}]"))
	f.Fuzz(func(t *testing.T, baseManifests, headManifests []byte) {
		r, err := Estimate(baseManifests, headManifests, "default", prices)
		if err != nil {
			return
		}
		totals := []float64{r.Before, r.After, r.Delta}
		for _, it := range r.Items {
			totals = append(totals, it.Delta)
		}
		for _, v := range totals {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("estimate is not a number: %+v", r)
			}
		}
	})
}

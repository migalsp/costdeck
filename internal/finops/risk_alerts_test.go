package finops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDigestListsOpenRisks(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()
	st := RiskState{Open: map[string]OpenRisk{}, Fired: map[string]time.Time{}}
	for i := range 12 {
		r := OpenRisk{Namespace: "shop", Kind: "Deployment", Workload: fmt.Sprintf("svc-%02d", i), Container: "main", Code: "oom-killed", Message: "OOM-killed."}
		st.Open[r.Key()] = r
	}
	if err := SaveRiskState(ctx, c, "costdeck", st); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRiskState(ctx, c, "costdeck")
	if err != nil || len(loaded.Open) != 12 {
		t.Fatalf("loaded = %d risks, %v", len(loaded.Open), err)
	}
	d := WithOpenRisks(ctx, c, "costdeck", Digest{Currency: "USD"})
	md := d.Markdown()
	if !strings.Contains(md, "**Performance risks**\n- shop / svc-00 / main: OOM-killed.") || !strings.Contains(md, "- and 2 more on their namespace pages") || strings.Contains(md, "svc-10") {
		t.Errorf("digest = %s", md)
	}
	if empty := WithOpenRisks(ctx, c, "elsewhere", Digest{Currency: "USD"}); strings.Contains(empty.Markdown(), "Performance risks") {
		t.Error("a digest without open risks has no risks section")
	}
}

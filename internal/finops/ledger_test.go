package finops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

var errAPIDown = errors.New("the API server is unavailable")

func clientFailingGets() client.Client {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errAPIDown
		},
	}).Build()
}

func TestLoadLedger(t *testing.T) {
	ctx := context.Background()
	stored := func(raw string) client.Client {
		return testClient(&corev1.ConfigMap{Name: LedgerConfigMap, Namespace: "costdeck", Data: map[string]string{ledgerKey: raw}})
	}
	for _, tc := range []struct {
		name    string
		client  client.Client
		days    int
		wantErr string
	}{
		{"no ConfigMap yet is an empty history", testClient(), 0, ""},
		{"a ConfigMap without the key is an empty history", stored(""), 0, ""},
		{"a stored history is read", stored(`{"v":1,"currency":"USD","days":[{"date":"2026-10-01","hours":24}]}`), 1, ""},
		{"a history that does not parse is an error", stored("{oops"), 0, "could not read the cost history"},
		{"a failed read is an error, not an empty history", clientFailingGets(), 0, errAPIDown.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := LoadLedger(ctx, tc.client, "costdeck")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || l != nil {
					t.Fatalf("LoadLedger = %+v, %v; want no ledger and an error with %q", l, err, tc.wantErr)
				}
				return
			}
			if err != nil || l == nil || len(l.Days) != tc.days {
				t.Fatalf("LoadLedger = %+v, %v; want %d day(s)", l, err, tc.days)
			}
		})
	}
}

func TestSaveLedgerUpdatesAConfigMapWithoutData(t *testing.T) {
	ctx := context.Background()
	c := testClient(&corev1.ConfigMap{Name: LedgerConfigMap, Namespace: "costdeck", Labels: map[string]string{"kept": "yes"}})
	if err := SaveLedger(ctx, c, "costdeck", &Ledger{Version: 1, Currency: "GBP", Days: []Day{{Date: "2026-10-01", Hours: 24, Provisioned: 3}}}); err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	_ = c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: LedgerConfigMap}, &cm)
	l, err := LoadLedger(ctx, c, "costdeck")
	if err != nil || l.Currency != "GBP" || len(l.Days) != 1 || l.Days[0].Provisioned != 3 || cm.Labels["kept"] != "yes" {
		t.Errorf("saved ledger = %+v, %v, labels %v", l, err, cm.Labels)
	}
}

func TestSaveLedgerWritesNothingWhenItCannotRead(t *testing.T) {
	err := SaveLedger(context.Background(), clientFailingGets(), "costdeck", &Ledger{Days: []Day{{Date: "2026-10-01"}}})
	if !errors.Is(err, errAPIDown) {
		t.Fatalf("SaveLedger = %v, want the read error rather than a new ConfigMap", err)
	}
}

func TestEncodeRoundsAmountsAndDropsNegligibleNamespaces(t *testing.T) {
	l := &Ledger{Version: 1, Currency: "USD", Days: []Day{{
		Date: "2026-10-01", Hours: 23.99987, Provisioned: 1.234567, Storage: 0.123456, List: 2.000049,
		Namespaces: map[string]NamespaceDay{
			"shop":  {Cost: 0.333333, Used: 0.111111, Storage: 0.0123456},
			"idle":  {Cost: 0.00001, Used: 0.00001},
			"usage": {Used: 0.0002},
		},
		Schedules: map[string]float64{"group/dev": 0.987654321},
	}}}
	raw, err := l.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var got Ledger
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	d := got.Days[0]
	if d.Hours != 24 || d.Provisioned != 1.2346 || d.Storage != 0.1235 || d.List != 2 {
		t.Errorf("day totals = %+v, want hours to 3 digits and money to 4", d)
	}
	if shop := d.Namespaces["shop"]; shop.Cost != 0.3333 || shop.Used != 0.1111 || shop.Storage != 0.0123 {
		t.Errorf("shop = %+v", shop)
	}
	if _, kept := d.Namespaces["idle"]; kept {
		t.Error("a namespace whose cost rounds to nothing is kept")
	}
	if _, kept := d.Namespaces["usage"]; !kept {
		t.Error("a namespace with usage but no requests is dropped")
	}
	if d.Schedules["group/dev"] != 0.9877 {
		t.Errorf("schedules = %v", d.Schedules)
	}
	if l.Days[0].Provisioned != 1.234567 {
		t.Error("encoding changed the ledger in memory")
	}
}

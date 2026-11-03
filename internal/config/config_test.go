package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func fakeClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(funcs).Build()
}

func TestOperatorNamespaceFollowsPodNamespace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "finops-system")
	if got := OperatorNamespace(); got != "finops-system" {
		t.Errorf("OperatorNamespace() = %q, want the injected POD_NAMESPACE", got)
	}
	if got := Key(); got != (client.ObjectKey{Name: "default", Namespace: "finops-system"}) {
		t.Errorf("Key() = %v, want default in the operator namespace", got)
	}

	t.Setenv("POD_NAMESPACE", "")
	if got := OperatorNamespace(); got != "costdeck" {
		t.Errorf("OperatorNamespace() without POD_NAMESPACE = %q, want costdeck", got)
	}
}

func TestEmptyIsAnUnsavedSingletonWithoutSettings(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "finops-system")
	cfg := Empty()
	if cfg.Name != DefaultConfigName || cfg.Namespace != "finops-system" {
		t.Errorf("Empty() = %s/%s, want finops-system/default", cfg.Namespace, cfg.Name)
	}
	if cfg.ResourceVersion != "" || cfg.UID != "" {
		t.Errorf("Empty() must not look like a stored object: rv=%q uid=%q", cfg.ResourceVersion, cfg.UID)
	}
	if cfg.Spec.Auth.Entra != nil || cfg.Spec.Integrations.VictoriaMetrics != nil || cfg.Spec.Providers.AWS != nil {
		t.Errorf("Empty() carries settings: %+v", cfg.Spec)
	}
}

func TestGetReturnsAnEmptyConfigWhenTheSingletonIsMissing(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	c := fakeClient(t, interceptor.Funcs{},
		&finopsv1.CostDeckConfig{Name: "default", Namespace: "elsewhere", Spec: finopsv1.CostDeckConfigSpec{ClusterName: "wrong-namespace"}},
		&finopsv1.CostDeckConfig{Name: "other", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{ClusterName: "wrong-name"}},
	)

	cfg, err := Get(context.Background(), c)
	if err != nil {
		t.Fatalf("a missing singleton is not an error, got %v", err)
	}
	if cfg == nil || cfg.Name != "default" || cfg.Namespace != "costdeck" {
		t.Fatalf("Get() = %+v, want the empty singleton", cfg)
	}
	if cfg.Spec.ClusterName != "" {
		t.Errorf("Get() read a config that is not the singleton: cluster %q", cfg.Spec.ClusterName)
	}
	err = c.Get(context.Background(), Key(), &finopsv1.CostDeckConfig{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("Get() must not create the singleton, lookup afterwards = %v", err)
	}
}

func TestGetReturnsTheStoredSingleton(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	c := fakeClient(t, interceptor.Funcs{},
		&finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{ClusterName: "prod-eu"}})

	cfg, err := Get(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Spec.ClusterName != "prod-eu" || cfg.ResourceVersion == "" {
		t.Errorf("Get() = cluster %q rv %q, want the stored singleton", cfg.Spec.ClusterName, cfg.ResourceVersion)
	}
}

func TestGetReportsOtherReadErrors(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	forbidden := apierrors.NewForbidden(finopsv1.GroupVersion.WithResource("costdeckconfigs").GroupResource(), "default", errors.New("rbac"))
	c := fakeClient(t, interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return forbidden
		},
	})

	cfg, err := Get(context.Background(), c)
	if cfg != nil {
		t.Errorf("Get() must not hand out a config it could not read, got %+v", cfg)
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("Get() error = %v, want the API error wrapped", err)
	}
	if !strings.Contains(err.Error(), "costdeck/default") {
		t.Errorf("Get() error %q does not name the object", err)
	}
}

func TestSecretDataReadsOnlyTheOperatorNamespace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	c := fakeClient(t, interceptor.Funcs{},
		&corev1.Secret{Name: "aws", Namespace: "costdeck", Data: map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("AKIA")}},
		&corev1.Secret{Name: "foreign", Namespace: "team-a", Data: map[string][]byte{"token": []byte("x")}},
	)
	ctx := context.Background()

	data, err := SecretData(ctx, c, "aws")
	if err != nil || string(data["AWS_ACCESS_KEY_ID"]) != "AKIA" {
		t.Errorf("SecretData(aws) = %q, %v", data, err)
	}

	data, err = SecretData(ctx, c, "foreign")
	if err == nil || data != nil {
		t.Fatalf("a Secret outside the operator namespace was read: %q", data)
	}
	if !apierrors.IsNotFound(err) || !strings.Contains(err.Error(), "read secret costdeck/foreign") {
		t.Errorf("SecretData(foreign) error = %v, want a wrapped not-found naming costdeck/foreign", err)
	}

	if _, err := SecretData(ctx, c, ""); err == nil || err.Error() != "no secret configured" {
		t.Errorf("SecretData(\"\") error = %v, want no secret configured", err)
	}
}

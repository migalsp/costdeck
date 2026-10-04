// Package config resolves operator-wide settings: the namespace the operator runs in and
// the singleton CostDeckConfig that holds provider and integration configuration.
package config

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

const (
	// DefaultConfigName is the name of the singleton CostDeckConfig.
	DefaultConfigName = "default"

	// defaultOperatorNamespace is used when POD_NAMESPACE is not injected, e.g. `make run`.
	defaultOperatorNamespace = "costdeck"
)

// OperatorNamespace returns the namespace the operator runs in. Every CostDeck custom
// resource (groups, configs, NamespaceFinOps, the CostDeckConfig singleton) and every
// credentials Secret lives there.
func OperatorNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	return defaultOperatorNamespace
}

// Key returns the object key of the CostDeckConfig singleton.
func Key() client.ObjectKey {
	return client.ObjectKey{Name: DefaultConfigName, Namespace: OperatorNamespace()}
}

// Get fetches the CostDeckConfig singleton. A missing singleton is not an error: an empty
// config with defaults is returned instead, so callers never have to special-case a fresh
// installation.
func Get(ctx context.Context, c client.Reader) (*finopsv1.CostDeckConfig, error) {
	cfg := &finopsv1.CostDeckConfig{}
	if err := c.Get(ctx, Key(), cfg); err != nil {
		if apierrors.IsNotFound(err) {
			return Empty(), nil
		}
		return nil, fmt.Errorf("get CostDeckConfig %s: %w", Key(), err)
	}
	return cfg, nil
}

// Empty returns an unsaved CostDeckConfig singleton with no settings.
func Empty() *finopsv1.CostDeckConfig {
	return &finopsv1.CostDeckConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName, Namespace: OperatorNamespace()},
	}
}

// SecretData reads a Secret from the operator namespace. CostDeck only ever reads its own
// credentials Secrets, which is what lets RBAC on Secrets stay namespace-scoped.
func SecretData(ctx context.Context, c client.Reader, name string) (map[string][]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("no secret configured")
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: OperatorNamespace()}, secret); err != nil {
		return nil, fmt.Errorf("read secret %s/%s: %w", OperatorNamespace(), name, err)
	}
	return secret.Data, nil
}

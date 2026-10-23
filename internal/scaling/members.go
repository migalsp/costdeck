package scaling

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// errEmptySelector refuses a selector without requirements: by Kubernetes rules it would
// match every namespace, and the group would scale the whole cluster to zero.
var errEmptySelector = errors.New("namespaceSelector has no labels to match; an empty selector would select every namespace")

// SelectNamespaces returns, sorted, the namespaces a group's namespaceSelector matches.
// It never selects the operator's own namespace, Kubernetes' kube-* namespaces, one being
// deleted, or one labelled costdeck.io/schedule=off.
func SelectNamespaces(selector *metav1.LabelSelector, namespaces []corev1.Namespace, operatorNamespace string) ([]string, error) {
	if selector == nil {
		return nil, nil
	}
	if len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return nil, errEmptySelector
	}
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("namespaceSelector: %w", err)
	}
	var out []string
	for _, ns := range namespaces {
		switch {
		case ns.Name == operatorNamespace,
			strings.HasPrefix(ns.Name, "kube-"),
			ns.DeletionTimestamp != nil,
			ns.Labels[finopsv1.OptOutLabel] == "off",
			!sel.Matches(labels.Set(ns.Labels)):
			continue
		}
		out = append(out, ns.Name)
	}
	slices.Sort(out)
	return out, nil
}

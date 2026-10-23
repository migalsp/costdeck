package v1

import "slices"

// OptOutLabel set to "off" on a namespace keeps every namespaceSelector from selecting it.
const OptOutLabel = "costdeck.io/schedule"

// Members returns the namespaces the group scales: those spec.namespaces lists and those
// spec.namespaceSelector matched at the last reconcile, sorted and without duplicates.
func (g *ScalingGroup) Members() []string {
	out := make([]string, 0, len(g.Spec.Namespaces)+len(g.Status.SelectedNamespaces))
	out = append(out, g.Spec.Namespaces...)
	if g.Spec.NamespaceSelector != nil {
		out = append(out, g.Status.SelectedNamespaces...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

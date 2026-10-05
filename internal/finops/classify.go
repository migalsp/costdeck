package finops

import (
	"strings"
)

// Environments a namespace can be classified into.
const (
	EnvProduction    = "production"
	EnvNonProduction = "non-production"
	EnvSystem        = "system"
	EnvUnknown       = "unclassified"
)

// environmentLabels are read in order; the first one present decides.
var environmentLabels = []string{"environment", "env", "app.kubernetes.io/environment", "stage", "tier"}

// teamLabels name the owner of a namespace, in order of preference.
var teamLabels = []string{"team", "owner", "app.kubernetes.io/team", "cost-center", "costcenter", "business-unit", "department", "app.kubernetes.io/part-of"}

var productionWords = map[string]bool{"prod": true, "production": true, "prd": true, "live": true}

var nonProductionWords = map[string]bool{
	"dev": true, "develop": true, "development": true, "test": true, "testing": true, "qa": true,
	"stage": true, "staging": true, "stg": true, "uat": true, "sandbox": true, "preview": true,
	"demo": true, "perf": true, "load": true, "feature": true, "review": true, "ci": true, "tmp": true,
}

// systemNamespaces are cluster plumbing rather than workloads someone owns.
var systemNamespaces = map[string]bool{
	"kube-system": true, "kube-public": true, "kube-node-lease": true,
	"cert-manager": true, "ingress-nginx": true, "monitoring": true, "argocd": true, "flux-system": true,
	"istio-system": true, "gatekeeper-system": true, "kyverno": true, "keda": true, "karpenter": true,
	"local-path-storage": true, "costdeck": true,
}

// Environment classifies a namespace as production, non-production, system or
// unclassified: from a label when there is one, otherwise from the words in its name.
func Environment(name string, labels map[string]string) string {
	for _, key := range environmentLabels {
		if v, ok := labels[key]; ok {
			if env := environmentOf(strings.ToLower(v)); env != EnvUnknown {
				return env
			}
		}
	}
	if systemNamespaces[name] || strings.HasPrefix(name, "kube-") || strings.HasSuffix(name, "-system") {
		return EnvSystem
	}
	return environmentOf(name)
}

func environmentOf(value string) string {
	words := strings.FieldsFunc(value, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == '/' })
	for _, w := range words {
		if productionWords[w] {
			return EnvProduction
		}
	}
	for _, w := range words {
		if nonProductionWords[w] || strings.HasPrefix(w, "dev") || strings.HasPrefix(w, "test") {
			return EnvNonProduction
		}
	}
	return EnvUnknown
}

// Team returns the owner recorded in a namespace's labels, or "".
func Team(labels map[string]string) string {
	for _, key := range teamLabels {
		if v := labels[key]; v != "" {
			return v
		}
	}
	return ""
}

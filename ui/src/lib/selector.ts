import type { LabelSelector, ScalingGroup } from './types'

// groupNamespaces lists the namespaces a group scales: those it names and those its label
// selector matched, sorted and without duplicates (ScalingGroup.Members in the operator).
export function groupNamespaces(g: ScalingGroup): string[] {
  const out = new Set(g.spec.namespaces || [])
  if (g.spec.namespaceSelector) for (const ns of g.status?.selectedNamespaces || []) out.add(ns)
  return [...out].sort()
}

// formatSelector writes a label selector in kubectl's syntax, the one the selector field
// of the schedule editor takes: "env=preview,team in (shop,pay),!legacy".
export function formatSelector(sel?: LabelSelector): string {
  if (!sel) return ''
  const parts = Object.entries(sel.matchLabels || {}).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => `${k}=${v}`)
  for (const e of sel.matchExpressions || []) {
    switch (e.operator) {
      case 'In': parts.push(`${e.key} in (${(e.values || []).join(',')})`); break
      case 'NotIn': parts.push(`${e.key} notin (${(e.values || []).join(',')})`); break
      case 'Exists': parts.push(e.key); break
      case 'DoesNotExist': parts.push(`!${e.key}`); break
    }
  }
  return parts.join(',')
}

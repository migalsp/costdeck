import type { HibernateRule } from './types'

export type Preset = NonNullable<HibernateRule['preset']>

// presets are the operators internal/scaling/hibernate.go knows.
export const presets: { id: Preset; label: string; covers: string }[] = [
  { id: 'cnpg', label: 'CloudNativePG', covers: 'PostgreSQL clusters, hibernated with their volumes kept' },
  { id: 'zalando-postgres', label: 'Zalando Postgres', covers: 'PostgreSQL clusters, numberOfInstances set to zero' },
  { id: 'strimzi', label: 'Strimzi', covers: 'Kafka Connect, MirrorMaker 2 and Bridge; Kafka clusters themselves cannot be stopped' },
]

// parseNames reads the names field: names or globs such as pg-*, separated by commas or
// spaces; none means every resource of the kind.
export function parseNames(text: string): string[] | undefined {
  const names = text.split(/[\s,]+/).filter(Boolean)
  return names.length ? names : undefined
}

// fieldPath is a dotted path outside metadata, such as spec.replicas, as Go checks it.
function fieldPath(s: string): boolean {
  const keys = s.split('.')
  return keys.length > 1 && keys[0] !== 'metadata' && !keys.includes('')
}

// badGlob is what Go's path.Match refuses: an unfinished escape, an unclosed or empty
// character class, or a range missing an end.
function badGlob(g: string): boolean {
  let i = 0
  const classChar = (): boolean => {
    if (i >= g.length || g[i] === '-' || g[i] === ']') return false
    if (g[i] === '\\' && ++i >= g.length) return false
    i++
    return true
  }
  while (i < g.length) {
    if (g[i] === '\\') {
      i += 2
      if (i > g.length) return true
      continue
    }
    if (g[i] !== '[') {
      i++
      continue
    }
    i++
    if (g[i] === '^') i++
    for (let ranges = 0; ; ranges++) {
      if (i < g.length && g[i] === ']' && ranges > 0) {
        i++
        break
      }
      if (!classChar()) return true
      if (g[i] === '-') {
        i++
        if (!classChar()) return true
      }
    }
  }
  return false
}

// ruleIssue mirrors scaling.ValidateHibernate, so the editor flags what saving would refuse.
export function ruleIssue(r: HibernateRule): string | null {
  if (r.preset) {
    if (r.apiVersion || r.kind || r.replicasField || r.readyField) return 'a preset takes no apiVersion, kind or fields'
  } else {
    if (!r.apiVersion || !r.kind || !r.replicasField) return 'set the apiVersion, kind and replicas field'
    const [group, version, extra] = r.apiVersion.split('/')
    if (!group || !version || extra !== undefined) return `apiVersion ${r.apiVersion} must be group/version, such as redis.example.com/v1`
    if (!fieldPath(r.replicasField) || (r.readyField && !fieldPath(r.readyField))) return 'fields are dotted paths such as spec.replicas'
  }
  const glob = (r.names || []).find(badGlob)
  return glob !== undefined ? `${glob} is not a name or glob` : null
}

// hibernateIssue is the first problem in a list of rules, numbered as the operator does.
export function hibernateIssue(rules: HibernateRule[]): string | null {
  for (const [i, r] of rules.entries()) {
    const issue = ruleIssue(r)
    if (issue) return `rule ${i + 1}: ${issue}`
  }
  return null
}

// describeRule says what a rule stops, for schedule cards.
export function describeRule(r: HibernateRule): string {
  const what = r.preset ? presets.find(p => p.id === r.preset)?.label ?? r.preset : `${r.kind} (${r.apiVersion})`
  return r.names?.length ? `${what} named ${r.names.join(', ')}` : what
}

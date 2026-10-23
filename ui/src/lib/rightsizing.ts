// Acting on right-sizing advice: applying it to a workload, opening a pull request with
// it, and the changes on record. Mirrors rightsizingChanges and RightsizingSettings in
// internal/api/rightsizing_actions.go.
import { when } from './schedule'

export interface ResourceValues { cpuRequest?: string; cpuLimit?: string; memoryRequest?: string; memoryLimit?: string }

export interface RightsizingChange {
  namespace: string
  name: string
  kind: string
  original: ResourceValues
  optimized: ResourceValues
  containers?: { name: string; original: ResourceValues; optimized: ResourceValues }[]
  appliedAt?: string
  appliedBy?: string
  state?: 'applied' | 'kept' | 'reverted' | 'proposed'
  reason?: string
  pullRequest?: string
}

export interface RightsizingChanges { applyEnabled: boolean; guardMinutes: number; repository?: string; changes: RightsizingChange[] }

// latest is the newest change of a workload.
export const latest = (r: RightsizingChanges | null, kind: string, name: string) => r?.changes.find(c => c.kind === kind && c.name === name)

// describeValues says what a change does: "main: CPU 2 → 150m, memory 4Gi → 384Mi".
export function describeValues(c: RightsizingChange): string {
  const containers = c.containers?.length ? c.containers : [{ name: '', original: c.original, optimized: c.optimized }]
  return containers.map(k => {
    const parts: string[] = []
    if (k.original.cpuRequest !== k.optimized.cpuRequest) parts.push(`CPU ${k.original.cpuRequest || 'unset'} → ${k.optimized.cpuRequest || 'unset'}`)
    if (k.original.memoryRequest !== k.optimized.memoryRequest) parts.push(`memory ${k.original.memoryRequest || 'unset'} → ${k.optimized.memoryRequest || 'unset'}`)
    return (k.name && containers.length > 1 ? `${k.name}: ` : '') + (parts.join(', ') || 'no change')
  }).join('; ')
}

// describeState says where a change stands, in a line.
export function describeState(c: RightsizingChange, guardMinutes: number, now = new Date()): string {
  const by = c.appliedBy ? ` by ${c.appliedBy}` : ''
  const at = c.appliedAt ? ` ${when(c.appliedAt, now)}` : ''
  switch (c.state) {
    case 'applied': {
      const until = c.appliedAt ? new Date(new Date(c.appliedAt).getTime() + guardMinutes * 60000).toISOString() : ''
      return `Applied${by}${at}; its new pods are watched${until ? ` until ${when(until, now)}` : ''} and get the old requests back if one fails`
    }
    case 'kept': return `Applied${by}${at}, and its pods stayed healthy`
    case 'reverted': return `Put back: ${c.reason || 'by hand'}`
    case 'proposed': return `Pull request opened${by}${at}`
  }
  return `Applied${by}${at}`
}

export interface RightsizingForm {
  apply: boolean
  guardMinutes: number
  git: { provider: 'github' | 'gitlab' | 'gitea'; repository: string; apiUrl: string; baseBranch: string; path: string; token: string; hasToken: boolean } | null
}

export interface RightsizingSettings {
  apply: boolean
  guardMinutes: number
  git?: { provider: 'github' | 'gitlab' | 'gitea'; repository: string; apiUrl?: string; baseBranch: string; path?: string; hasToken: boolean }
}

export const rightsizingForm = (s?: RightsizingSettings): RightsizingForm => ({
  apply: !!s?.apply, guardMinutes: s?.guardMinutes || 30,
  git: s?.git ? { provider: s.git.provider, repository: s.git.repository, apiUrl: s.git.apiUrl || '', baseBranch: s.git.baseBranch || 'main', path: s.git.path || '', token: '', hasToken: s.git.hasToken } : null,
})

export const gitRequest = (g: NonNullable<RightsizingForm['git']>) => ({
  provider: g.provider, repository: g.repository.trim(), apiUrl: g.apiUrl.trim(), baseBranch: g.baseBranch.trim(), path: g.path.trim(),
  ...(g.token.trim() ? { token: g.token.trim() } : {}),
})

// rightsizingRequest is what Save sends; a removed repository is removed.
export const rightsizingRequest = (f: RightsizingForm) => ({
  apply: f.apply, guardMinutes: f.guardMinutes, ...(f.git ? { git: gitRequest(f.git) } : { removeGit: true }),
})

export function rightsizingProblem(f: RightsizingForm): string | null {
  const g = f.git
  if (!g) return null
  if (!g.repository.trim()) return 'Right-sizing: enter the repository, such as acme/apps'
  if (g.provider === 'gitea' && !g.apiUrl.trim()) return 'Right-sizing: a Gitea needs its API URL, such as https://gitea.example.com/api/v1'
  if (g.apiUrl.trim() && !/^https?:\/\/[^\s/]+/.test(g.apiUrl.trim())) return 'Right-sizing: the API URL starts with https://'
  if (!g.hasToken && !g.token.trim()) return 'Right-sizing: enter an access token that can push branches and open pull requests'
  return null
}

export const apiPlaceholder: Record<'github' | 'gitlab' | 'gitea', string> = {
  github: 'https://api.github.com (GitHub Enterprise: https://github.example.com/api/v3)',
  gitlab: 'https://gitlab.com/api/v4',
  gitea: 'https://gitea.example.com/api/v1',
}

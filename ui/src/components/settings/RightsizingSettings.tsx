import { useState } from 'react'
import { AlertTriangle, GitPullRequest, Wand2 } from 'lucide-react'
import { apiError, errorMessage } from '../../lib/api'
import { apiPlaceholder, gitRequest, type RightsizingForm } from '../../lib/rightsizing'
import { Button } from '../ui'
import { SecretInput, SettingsCard, TestButton, inputClass, labelClass } from './fields'

const guardChoices = [15, 30, 60, 120]

// RightsizingSettings decides what operators can do with right-sizing advice: apply it to
// workloads, or open pull requests with it in a GitOps repository. The section's Save
// stores it.
export default function RightsizingSettings({ value, onChange }: { value: RightsizingForm; onChange: (f: RightsizingForm) => void }) {
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)
  const set = (patch: Partial<RightsizingForm>) => onChange({ ...value, ...patch })
  const g = value.git
  const setGit = (patch: Partial<NonNullable<RightsizingForm['git']>>) => g && set({ git: { ...g, ...patch } })

  const test = async () => {
    if (!g) return
    setTesting(true)
    setResult(null)
    try {
      const res = await fetch('/api/settings/providers/git/test', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(gitRequest(g)) })
      if (!res.ok) throw new Error(await apiError(res))
      const d = await res.json()
      setResult({ ok: !!d.connected, text: d.connected ? d.message : d.error || 'Could not reach it' })
    } catch (e) {
      setResult({ ok: false, text: errorMessage(e) })
    } finally {
      setTesting(false)
    }
  }

  return (
    <>
      <SettingsCard logo={<div className="w-10 h-10 rounded-xl bg-brand-50 flex items-center justify-center"><Wand2 size={20} className="text-brand-600" /></div>}
        title="Apply advice to workloads" subtitle="Off by default. Operators apply advice from a namespace's page." enabled={value.apply} onToggle={v => set({ apply: v })}>
        {value.apply && (
          <div className="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800" role="alert">
            <div className="flex gap-2 font-semibold"><AlertTriangle size={16} className="mt-0.5 shrink-0" />Applying changes pod requests</div>
            <ul className="mt-1.5 ml-6 list-disc space-y-0.5 text-[13px]">
              <li>The workload rolls out new pods with the recommended requests; limits are left as they are.</li>
              <li>Its new pods are watched; if one is OOMKilled, restarts or crash-loops, the old requests come back and the channels that take alerts are told.</li>
              <li>Workloads a GitOps tool manages are left alone, since it would put the old requests back: open a pull request for them.</li>
            </ul>
          </div>
        )}
        <div>
          <label className={labelClass} htmlFor="guard-minutes">Watch new pods for</label>
          <select id="guard-minutes" className={`${inputClass} max-w-[12rem]`} value={value.guardMinutes} onChange={e => set({ guardMinutes: Number(e.target.value) })}>
            {(guardChoices.includes(value.guardMinutes) ? guardChoices : [value.guardMinutes, ...guardChoices]).map(m => <option key={m} value={m}>{m < 60 ? `${m} minutes` : `${m / 60} hour${m === 60 ? '' : 's'}`}</option>)}
          </select>
        </div>
      </SettingsCard>

      <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
        <div className="flex items-center justify-between gap-3 mb-5">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-slate-100 flex items-center justify-center"><GitPullRequest size={20} className="text-slate-500" /></div>
            <div>
              <span className="font-bold text-slate-800">Pull requests in a GitOps repository</span>
              <p className="text-[11px] text-slate-400 mt-0.5">Cost Deck finds the workload's manifest, sets its requests and opens a pull request with the evidence.</p>
            </div>
          </div>
          {g
            ? <Button size="sm" variant="ghost" onClick={() => { set({ git: null }); setResult(null) }}>Remove</Button>
            : <Button size="sm" onClick={() => set({ git: { provider: 'github', repository: '', apiUrl: '', baseBranch: 'main', path: '', token: '', hasToken: false } })}>Add a repository</Button>}
        </div>
        {g && (
          <div className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-[10rem_1fr] gap-3">
              <div>
                <label className={labelClass} htmlFor="git-provider">Provider</label>
                <select id="git-provider" className={inputClass} value={g.provider} onChange={e => setGit({ provider: e.target.value as 'github' | 'gitlab' | 'gitea' })}>
                  <option value="github">GitHub</option>
                  <option value="gitlab">GitLab</option>
                  <option value="gitea">Gitea / Forgejo</option>
                </select>
              </div>
              <div>
                <label className={labelClass} htmlFor="git-repository">Repository</label>
                <input id="git-repository" className={`${inputClass} font-mono`} value={g.repository} onChange={e => setGit({ repository: e.target.value })} placeholder={g.provider === 'gitlab' ? 'group/project' : 'owner/name'} />
              </div>
            </div>
            <div>
              <label className={labelClass} htmlFor="git-api">API URL {g.provider !== 'gitea' && <span className="normal-case font-normal text-slate-400">(empty: the public service)</span>}</label>
              <input id="git-api" className={`${inputClass} font-mono`} value={g.apiUrl} onChange={e => setGit({ apiUrl: e.target.value })} placeholder={apiPlaceholder[g.provider]} />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label className={labelClass} htmlFor="git-branch">Base branch</label>
                <input id="git-branch" className={`${inputClass} font-mono`} value={g.baseBranch} onChange={e => setGit({ baseBranch: e.target.value })} placeholder="main" />
              </div>
              <div>
                <label className={labelClass} htmlFor="git-path">Manifests under <span className="normal-case font-normal text-slate-400">(empty: the whole repository)</span></label>
                <input id="git-path" className={`${inputClass} font-mono`} value={g.path} onChange={e => setGit({ path: e.target.value })} placeholder="deploy/" />
              </div>
            </div>
            <div>
              <label className={labelClass}>Access token {g.hasToken && <span className="ml-2 text-emerald-500 normal-case font-medium text-xs">✓ Configured</span>}</label>
              <SecretInput value={g.token} onChange={v => setGit({ token: v })} label="Git access token" placeholder={g.hasToken ? '•••••••• (leave empty to keep it)' : 'A token that can push branches and open pull requests'} />
              <p className="mt-1 text-[11px] text-slate-400">Manifests rendered from a Helm chart keep their requests in values files, which Cost Deck cannot map to a workload; plain YAML and kustomize work.</p>
            </div>
            <TestButton onTest={test} testing={testing} result={result} />
          </div>
        )}
      </div>
    </>
  )
}

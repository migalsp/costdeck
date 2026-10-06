import { useState } from 'react'
import { AlertTriangle, BellRing, Loader2, Pencil, Plus, Target, TrendingUp, Wallet } from 'lucide-react'
import { Badge, Button, Card, Modal, PageHeader, SectionTitle, type Tone } from '../components/ui'
import { useAuth } from '../lib/auth'
import { environmentLabel, fetchBudgets, saveSettings, type AnomalySettings, type Budget, type BudgetScope, type BudgetStatus, type BudgetsResponse, type Environment } from '../lib/finops'
import { formatMoney } from '../lib/format'
import { errorMessage } from '../lib/api'
import { usePolling } from '../lib/usePolling'

const stateLabel: Record<BudgetStatus['state'], { label: string; tone: Tone }> = {
  ok: { label: 'On track', tone: 'success' },
  'at-risk': { label: 'Heading over', tone: 'warning' },
  over: { label: 'Over budget', tone: 'danger' },
}

const scopeText = (b: Budget) => {
  switch (b.scope) {
    case 'cluster': return 'Whole cluster'
    case 'namespace': return `Namespace ${b.value}`
    case 'team': return `Team ${b.value}`
    case 'environment': return `${environmentLabel[b.value as Environment] || b.value} namespaces`
  }
}

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

// Mark is a hoverable marker on a budget bar with a tooltip naming it.
function Mark({ left, label, children }: { left: string; label: string; children: React.ReactNode }) {
  return (
    <div className="group absolute top-1/2 -translate-y-1/2 -translate-x-1/2 px-1.5 cursor-default" style={{ left }}>
      {children}
      <span role="tooltip" className="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1 hidden group-hover:block whitespace-nowrap rounded-md bg-slate-900 px-2 py-1 text-[11px] text-white shadow-lg z-30">{label}</span>
    </div>
  )
}

function BudgetBar({ s, currency }: { s: BudgetStatus; currency: string }) {
  const top = Math.max(s.limit, s.forecast, s.spent) * 1.05 || 1
  const pct = (v: number) => `${Math.min(100, (v / top) * 100)}%`
  const fill = s.state === 'over' ? 'bg-rose-500' : s.state === 'at-risk' ? 'bg-amber-500' : 'bg-brand-700'
  const thresholds = s.budget.thresholds?.length ? s.budget.thresholds : [80, 100]
  return (
    <div className="relative h-3 rounded-full bg-slate-100 mt-3">
      <div className={`absolute inset-y-0 left-0 rounded-full ${fill}`} style={{ width: pct(s.spent) }} />
      {thresholds.map(t => (
        <Mark key={t} left={pct(s.limit * t / 100)} label={`Alert at ${t}% · ${formatMoney(s.limit * t / 100, currency)}`}>
          <div className="w-px h-5 bg-slate-500" />
        </Mark>
      ))}
      <Mark left={pct(s.forecast)} label={`Forecast for the month · ${formatMoney(s.forecast, currency)}`}>
        <div className="h-6 border-l-2 border-dashed border-slate-700" />
      </Mark>
    </div>
  )
}

function BudgetCard({ s, currency, onEdit }: { s: BudgetStatus; currency: string; onEdit?: () => void }) {
  const st = stateLabel[s.state]
  return (
    <Card className="p-5">
      <div className="flex items-start gap-2">
        <div className="min-w-0">
          <div className="font-semibold text-slate-800 truncate">{s.budget.name}</div>
          <div className="text-xs text-slate-500">{scopeText(s.budget)}{s.budget.scope !== 'cluster' && ` · ${s.namespaces.length} namespace${s.namespaces.length === 1 ? '' : 's'}`}</div>
        </div>
        <div className="ml-auto flex items-center gap-1">
          <Badge tone={st.tone}>{st.label}</Badge>
          {onEdit && <button onClick={onEdit} className="p-1 rounded text-slate-400 hover:text-slate-700 hover:bg-slate-100" title="Edit budget"><Pencil size={14} /></button>}
        </div>
      </div>
      <div className="mt-3 flex items-baseline gap-1.5">
        <span className="text-2xl font-bold tracking-tight text-slate-900">{formatMoney(s.spent, currency)}</span>
        <span className="text-sm text-slate-500">of {formatMoney(s.limit, currency)} this month</span>
      </div>
      <BudgetBar s={s} currency={currency} />
      <div className="mt-2 flex justify-between text-xs text-slate-500">
        <span>{s.limit ? `${Math.round((s.spent / s.limit) * 100)}% used` : ''}</span>
        <span className="inline-flex items-center gap-1"><TrendingUp size={12} /> heading for <b className="text-slate-700">{formatMoney(s.forecast, currency)}</b></span>
      </div>
      {s.partial && <p className="mt-2 text-[11px] text-slate-400">The cost history starts after the 1st, so the month so far is incomplete.</p>}
    </Card>
  )
}

function BudgetEditor({ initial, existing, scopes, onClose, onSave }: {
  initial?: Budget
  existing: Budget[]
  scopes: BudgetsResponse['scopes']
  onClose: () => void
  onSave: (budgets: Budget[]) => Promise<void>
}) {
  const [b, setB] = useState<Budget>(initial || { name: '', scope: 'team', value: scopes.teams[0] || '', monthlyLimit: '', thresholds: [80, 100], forecast: true })
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const set = (patch: Partial<Budget>) => setB(prev => ({ ...prev, ...patch }))
  const options: Record<BudgetScope, string[]> = {
    cluster: [],
    namespace: scopes.namespaces,
    team: scopes.teams,
    environment: ['production', 'non-production', 'system', 'unclassified'],
  }
  const others = existing.filter(x => x.name !== initial?.name)
  const submit = async (list: Budget[]) => {
    setSaving(true)
    setError(null)
    try {
      await onSave(list)
      onClose()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }
  const toggleThreshold = (t: number) => {
    const cur = b.thresholds || []
    set({ thresholds: cur.includes(t) ? cur.filter(x => x !== t) : [...cur, t].sort((x, y) => x - y) })
  }
  const field = 'w-full px-3 py-2 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500'
  return (
    <Modal title={initial ? `Budget ${initial.name}` : 'New budget'} subtitle="A monthly limit with alerts to the Webex space" size="md" onClose={onClose}
      footer={<>
        {initial && (confirmDelete
          ? <Button variant="danger" onClick={() => submit(others)} disabled={saving}>Delete for good</Button>
          : <Button variant="ghost" onClick={() => setConfirmDelete(true)}>Delete</Button>)}
        <div className="ml-auto flex gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={saving || !b.name || !b.monthlyLimit || (b.scope !== 'cluster' && !b.value)}
            onClick={() => submit([...others, { ...b, value: b.scope === 'cluster' ? undefined : b.value }])}>{saving ? 'Saving…' : 'Save budget'}</Button>
        </div>
      </>}>
      <div className="space-y-4">
        <label className="block">
          <span className="text-sm font-medium text-slate-700">Name</span>
          <input className={field} value={b.name} disabled={!!initial} placeholder="payments" onChange={e => set({ name: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, '-') })} />
        </label>
        <div className="grid grid-cols-2 gap-3">
          <label className="block">
            <span className="text-sm font-medium text-slate-700">Covers</span>
            <select className={field} value={b.scope} onChange={e => { const scope = e.target.value as BudgetScope; set({ scope, value: options[scope][0] || '' }) }}>
              <option value="team">A team</option>
              <option value="namespace">A namespace</option>
              <option value="environment">An environment</option>
              <option value="cluster">The whole cluster</option>
            </select>
          </label>
          {b.scope !== 'cluster' && (
            <label className="block">
              <span className="text-sm font-medium text-slate-700">{b.scope === 'team' ? 'Team' : b.scope === 'namespace' ? 'Namespace' : 'Environment'}</span>
              {options[b.scope].length > 0 ? (
                <select className={field} value={b.value} onChange={e => set({ value: e.target.value })}>
                  {options[b.scope].map(o => <option key={o} value={o}>{b.scope === 'environment' ? environmentLabel[o as Environment] : o}</option>)}
                </select>
              ) : <input className={field} value={b.value || ''} placeholder={b.scope === 'team' ? 'team label value' : ''} onChange={e => set({ value: e.target.value })} />}
            </label>
          )}
        </div>
        {b.scope === 'team' && scopes.teams.length === 0 && <p className="text-xs text-slate-500">No namespace has a team label yet: label them with <code className="px-1 rounded bg-slate-100">team=&lt;name&gt;</code>.</p>}
        <label className="block">
          <span className="text-sm font-medium text-slate-700">Monthly limit</span>
          <input className={field} inputMode="decimal" value={b.monthlyLimit} placeholder="1200" onChange={e => set({ monthlyLimit: e.target.value.replace(/[^0-9.]/g, '') })} />
        </label>
        <div>
          <span className="text-sm font-medium text-slate-700">Alert when spending reaches</span>
          <div className="mt-1.5 flex flex-wrap gap-2">
            {[50, 80, 90, 100].map(t => (
              <label key={t} className={`px-3 py-1.5 rounded-lg border text-sm cursor-pointer ${b.thresholds?.includes(t) ? 'border-brand-300 bg-brand-50 text-brand-800' : 'border-slate-200 text-slate-600'}`}>
                <input type="checkbox" className="sr-only" checked={!!b.thresholds?.includes(t)} onChange={() => toggleThreshold(t)} />{t}%
              </label>
            ))}
          </div>
        </div>
        <label className="flex items-center gap-2 text-sm text-slate-700">
          <input type="checkbox" className="accent-brand-600" checked={!!b.forecast} onChange={e => set({ forecast: e.target.checked })} />
          Also alert once when the month is heading over the limit
        </label>
        {error && <p className="text-sm text-rose-600">{error}</p>}
      </div>
    </Modal>
  )
}

function AnomalyCard({ settings, onSaved }: { settings: AnomalySettings; onSaved: () => void }) {
  const { can } = useAuth()
  const [draft, setDraft] = useState<AnomalySettings | null>(null)
  const [error, setError] = useState<string | null>(null)
  const a = draft || settings
  const save = async () => {
    try {
      await saveSettings({ alerts: { anomalies: { enabled: !!a.enabled, percent: a.percent || 30, minimumDaily: a.minimumDaily || '1' } } })
      setDraft(null)
      setError(null)
      onSaved()
    } catch (e) {
      setError(errorMessage(e))
    }
  }
  const edit = (patch: AnomalySettings) => setDraft({ ...a, ...patch })
  return (
    <Card className="p-5">
      <div className="flex items-center gap-2">
        <BellRing size={17} className="text-brand-600" />
        <h2 className="text-base font-semibold text-slate-800">Anomaly alerts</h2>
        <Badge tone={settings.enabled ? 'success' : 'neutral'}>{settings.enabled ? 'On' : 'Off'}</Badge>
      </div>
      <p className="mt-2 text-sm text-slate-600">
        Every morning each namespace's cost for the day before is compared with its average over the seven days before that.
      </p>
      {can('admin') ? (
        <div className="mt-4 space-y-3">
          <label className="flex items-center gap-2 text-sm text-slate-700">
            <input type="checkbox" className="accent-brand-600" checked={!!a.enabled} onChange={e => edit({ enabled: e.target.checked })} /> Send anomaly alerts
          </label>
          <div className="flex flex-wrap items-center gap-2 text-sm text-slate-600">
            When a day costs
            <input type="number" min={5} max={1000} value={a.percent || 30} onChange={e => edit({ percent: Number(e.target.value) })}
              className="w-16 px-2 py-1 border border-slate-200 rounded-md text-center" />
            % more than usual, and at least
            <input inputMode="decimal" value={a.minimumDaily || '1'} onChange={e => edit({ minimumDaily: e.target.value.replace(/[^0-9.]/g, '') })}
              className="w-16 px-2 py-1 border border-slate-200 rounded-md text-center" />
            more in money.
          </div>
          {error && <p className="text-xs text-rose-600">{error}</p>}
          <Button size="sm" variant="primary" disabled={!draft} onClick={save}>Save</Button>
        </div>
      ) : (
        <p className="mt-2 text-xs text-slate-500">Threshold {settings.percent || 30}%, at least {settings.minimumDaily || '1'} a day.</p>
      )}
    </Card>
  )
}

export default function BudgetsPage() {
  const { can } = useAuth()
  const [data, setData] = useState<BudgetsResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<Budget | 'new' | null>(null)

  const load = () => fetchBudgets().then(d => { setData(d); setError(null) }).catch(e => setError(errorMessage(e)))
  usePolling(load, 60000)

  if (!data) {
    return (
      <div className="p-8 max-w-[1600px] mx-auto">
        <PageHeader title="Budgets & Alerts" subtitle="Monthly limits for teams, namespaces and environments, and alerts when spending jumps" />
        {error ? <p className="text-sm text-rose-600">Could not load budgets: {error}</p>
          : <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={16} className="animate-spin" /> Loading…</div>}
      </div>
    )
  }

  const budgets = data.budgets.map(s => s.budget)
  const save = async (list: Budget[]) => { await saveSettings({ budgets: list }); await load() }

  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      <PageHeader title="Budgets & Alerts" subtitle="Monthly limits for teams, namespaces and environments, and alerts when spending jumps"
        actions={can('admin') ? <Button variant="primary" icon={<Plus size={16} />} onClick={() => setEditing('new')}>New budget</Button> : undefined} />

      {!data.webexReady && (
        <div className="mb-4 flex gap-2 p-3 rounded-xl border border-slate-200 bg-slate-50 text-sm text-slate-600">
          <AlertTriangle size={16} className="mt-0.5 shrink-0 text-slate-400" />
          <span>Alerts are listed below and exported as Prometheus metrics. To receive them in Webex, connect it with a space ID under Settings → Messengers.</span>
        </div>
      )}

      {data.budgets.length === 0 ? (
        <Card className="p-10 text-center">
          <Target size={36} className="mx-auto text-slate-300 mb-3" />
          <p className="font-medium text-slate-700">No budgets yet</p>
          <p className="text-sm text-slate-500 mt-1">Give a team, a namespace or the whole cluster a monthly limit; Cost Deck alerts at the thresholds you choose and when the month is heading over.</p>
          {can('admin') && <Button variant="primary" className="mt-4" icon={<Plus size={16} />} onClick={() => setEditing('new')}>New budget</Button>}
        </Card>
      ) : (
        <>
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {data.budgets.map(s => <BudgetCard key={s.budget.name} s={s} currency={data.currency} onEdit={can('admin') ? () => setEditing(s.budget) : undefined} />)}
          </div>
          <div className="mt-3 flex flex-wrap gap-x-5 gap-y-1 text-[11px] text-slate-500">
            <span className="inline-flex items-center gap-1.5"><span className="w-2.5 h-2.5 rounded-sm bg-brand-700" />Spent this month</span>
            <span className="inline-flex items-center gap-1.5"><span className="w-px h-3 bg-slate-500" />Alert thresholds</span>
            <span className="inline-flex items-center gap-1.5"><span className="h-3 border-l-2 border-dashed border-slate-700" />Forecast for the month</span>
            <span>Hover a mark for its amount.</span>
          </div>
        </>
      )}

      <div className="grid gap-4 mt-8 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <SectionTitle aside={<span className="text-xs text-slate-400">the last 100</span>}>Recent alerts</SectionTitle>
          {data.events.length === 0 ? (
            <Card className="p-6 text-sm text-slate-500">No alert has fired yet.</Card>
          ) : (
            <Card className="divide-y divide-slate-100">
              {data.events.map((ev, i) => (
                <div key={i} className="flex items-start gap-3 p-4">
                  <span className={`mt-0.5 w-8 h-8 shrink-0 rounded-lg flex items-center justify-center ${ev.kind === 'anomaly' ? 'bg-amber-50 text-amber-600' : 'bg-brand-50 text-brand-700'}`}>
                    {ev.kind === 'anomaly' ? <TrendingUp size={16} /> : <Wallet size={16} />}
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-semibold text-slate-800">{ev.title}</div>
                    <div className="text-xs text-slate-500 mt-0.5">{ev.detail}</div>
                  </div>
                  <div className="shrink-0 text-right text-[11px] text-slate-400">
                    {when(ev.at)}
                    <div>{ev.delivered ? `sent to ${ev.delivered}` : 'recorded'}</div>
                  </div>
                </div>
              ))}
            </Card>
          )}
        </div>
        <div>
          <SectionTitle>Settings</SectionTitle>
          <AnomalyCard settings={data.anomalies || {}} onSaved={load} />
        </div>
      </div>

      {editing && (
        <BudgetEditor initial={editing === 'new' ? undefined : editing} existing={budgets} scopes={data.scopes}
          onClose={() => setEditing(null)} onSave={save} />
      )}
    </div>
  )
}

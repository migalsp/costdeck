import { useState } from 'react'
import { CalendarClock, Check, Copy, Download, FileText, Loader2, Printer, Send, Sparkles, Trash2 } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { apiError, errorMessage } from '../lib/api'
import { useAuth } from '../lib/auth'
import { Badge, Button, Card, PageHeader, SectionTitle, Tabs } from '../components/ui'
import { Delta, KpiTile } from '../components/finops/charts'
import {
  change as relChange, downloadText, efficiencyRating, fetchDigest, fetchReport, fetchReports, percent,
  type Audience, type Digest, type DigestRow, type DigestSchedule, type ReportEntry, type ReportsIndex,
} from '../lib/finops'
import { formatMoney } from '../lib/format'
import { usePolling } from '../lib/usePolling'

const audiences: { id: Audience; label: string; hint: string }[] = [
  { id: 'executive', label: 'Executive', hint: 'One page for leadership and finance: run rate, trend, savings, three decisions.' },
  { id: 'engineering', label: 'Engineering', hint: 'Waste by namespace, right-sizing, scheduling, nodes and hygiene, as a checklist.' },
  { id: 'showback', label: 'Team showback', hint: 'Cost and efficiency per team, with what each team can do.' },
]

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : '')

function RowsTable({ title, rows, currency }: { title: string; rows: DigestRow[]; currency: string }) {
  return (
    <div>
      <h3 className="text-sm font-semibold text-slate-800 mb-2">{title}</h3>
      <div className="rounded-xl border border-slate-200 overflow-hidden">
        <table className="w-full text-sm">
          <tbody>
            {rows.map(r => (
              <tr key={r.name} className="border-t border-slate-100 first:border-t-0">
                <td className="px-3 py-2 text-slate-700 truncate max-w-[14rem]">{r.name}</td>
                <td className="px-3 py-2 text-right font-semibold tabular-nums text-slate-800">{formatMoney(r.cost, currency)}</td>
                <td className="px-3 py-2 text-right w-24">{r.prev ? <Delta value={relChange(r.cost, r.prev)} goodWhen="down" compact /> : <span className="text-xs text-slate-400">new</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function DigestView({ d }: { d: Digest }) {
  const cur = d.currency
  if (d.hours === 0) {
    return <p className="text-sm text-slate-500 py-6">The cost history does not cover this period yet: Cost Deck started recording after it ended. Pick a shorter period or come back once it has run for a full {d.period}.</p>
  }
  return (
    <div>
      {!d.complete && <p className="mb-4 text-xs text-amber-800 bg-amber-50 border border-amber-200 rounded-lg px-3 py-2">The cost history does not cover both periods completely ({d.hours.toFixed(0)} of the hours in this {d.period}), so the comparison is partial.</p>}
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <KpiTile label="Node cost" value={formatMoney(d.cost, cur)}
          delta={d.prevCost ? <Delta value={relChange(d.cost, d.prevCost)} goodWhen="down" label={`vs the ${d.period} before`} /> : undefined}
          sub={d.prevCost ? undefined : 'No earlier period to compare'} />
        <KpiTile label="Used by pods" value={percent(d.cost ? d.used / d.cost : undefined)} status={efficiencyRating(d.cost ? d.used / d.cost : undefined)}
          sub={`${formatMoney(d.requested, cur)} requested, ${formatMoney(d.used, cur)} used`} />
        <KpiTile label="Saved by schedules" tone={d.saved > 0 ? 'good' : undefined} value={formatMoney(d.saved, cur)}
          delta={d.prevSaved ? <Delta value={relChange(d.saved, d.prevSaved)} goodWhen="up" label={`vs the ${d.period} before`} /> : undefined} />
        <KpiTile label="This month" value={d.monthToDate.hours > 0 ? formatMoney(d.monthToDate.cost, cur) : '—'} sub={d.monthToDate.hours > 0 ? `Heading for ${formatMoney(d.monthToDate.forecast, cur)}` : 'No history this month yet'} />
      </div>
      <div className="grid gap-4 mt-5 lg:grid-cols-2">
        {d.namespaces.length > 0 && <RowsTable title="Most expensive namespaces" rows={d.namespaces} currency={cur} />}
        {d.teams.length > 0 && <RowsTable title="By team" rows={d.teams} currency={cur} />}
      </div>
      {d.opportunities.length > 0 && (
        <div className="mt-5">
          <h3 className="text-sm font-semibold text-slate-800 mb-2">Worth doing</h3>
          <ul className="space-y-1.5">
            {d.opportunities.map((o, i) => (
              <li key={i} className="flex items-baseline gap-3 text-sm">
                <span className="text-slate-700">{o.title}</span>
                <span className="ml-auto font-semibold text-brand-700 tabular-nums whitespace-nowrap">−{formatMoney(o.monthlySavings || 0, cur)}/mo</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

function ScheduleCard({ index, onSaved }: { index: ReportsIndex; onSaved: () => void }) {
  const { can } = useAuth()
  const [draft, setDraft] = useState<DigestSchedule | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const s = draft || index.schedule
  const tz = s.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  const edit = (patch: Partial<DigestSchedule>) => setDraft({ ...s, timezone: tz, frequency: s.frequency || 'weekly', time: s.time || '09:00', ...patch })

  const save = async () => {
    setSaving(true)
    setError(null)
    try {
      const res = await fetch('/api/settings', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reports: { digest: { ...s, timezone: tz, frequency: s.frequency || 'weekly', time: s.time || '09:00' } } }),
      })
      if (!res.ok) throw new Error(await apiError(res))
      setDraft(null)
      onSaved()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  const describe = index.schedule.enabled
    ? `${index.schedule.frequency === 'monthly' ? 'On the 1st of every month' : 'Every Monday'} at ${index.schedule.time || '09:00'} ${index.schedule.timezone || 'UTC'}, to the Webex space.`
    : 'Not scheduled. Turn it on to post the digest to the Webex space every week or month.'
  return (
    <Card className="p-5">
      <div className="flex items-center gap-2">
        <CalendarClock size={17} className="text-brand-600" />
        <h2 className="text-base font-semibold text-slate-800">Scheduled digest</h2>
        <Badge tone={index.schedule.enabled ? 'success' : 'neutral'}>{index.schedule.enabled ? 'On' : 'Off'}</Badge>
      </div>
      <p className="mt-2 text-sm text-slate-600">{describe}</p>
      <div className="mt-1 text-xs text-slate-500 space-y-0.5">
        {index.nextDue && index.schedule.enabled && <div>Next: {when(index.nextDue)}</div>}
        {index.lastSent && <div>Last sent: {when(index.lastSent)}</div>}
      </div>
      {!index.webexReady && (
        <p className="mt-3 text-xs text-amber-800 bg-amber-50 border border-amber-200 rounded-lg px-3 py-2">
          The digest is posted to Webex. Enable Webex with a space ID under Settings → Notifications first.
        </p>
      )}
      {can('admin') && (
        <div className="mt-4 pt-4 border-t border-slate-100 space-y-3">
          <label className="flex items-center gap-2 text-sm text-slate-700">
            <input type="checkbox" className="accent-brand-600" checked={!!s.enabled} onChange={e => edit({ enabled: e.target.checked })} />
            Send the digest automatically
          </label>
          <div className="grid grid-cols-2 gap-2">
            <select value={s.frequency || 'weekly'} onChange={e => edit({ frequency: e.target.value as DigestSchedule['frequency'] })}
              className="py-1.5 px-2 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500">
              <option value="weekly">Weekly, on Mondays</option>
              <option value="monthly">Monthly, on the 1st</option>
            </select>
            <input type="time" value={s.time || '09:00'} onChange={e => edit({ time: e.target.value })}
              className="py-1.5 px-2 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500" />
          </div>
          <input value={tz} onChange={e => edit({ timezone: e.target.value })} placeholder="Europe/Berlin"
            className="w-full py-1.5 px-2 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500" />
          {error && <p className="text-xs text-rose-600">{error}</p>}
          <Button variant="primary" size="sm" disabled={!draft || saving} onClick={save}>{saving ? 'Saving…' : 'Save schedule'}</Button>
        </div>
      )}
    </Card>
  )
}

export default function ReportsPage() {
  const { can } = useAuth()
  const [index, setIndex] = useState<ReportsIndex | null>(null)
  const [period, setPeriod] = useState<'week' | 'month'>('week')
  const [digest, setDigest] = useState<{ digest: Digest; markdown: string } | null>(null)
  const [digestError, setDigestError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [sending, setSending] = useState(false)
  const [copied, setCopied] = useState(false)

  const [audience, setAudience] = useState<Audience>('executive')
  const [streaming, setStreaming] = useState<string | null>(null)
  const [genError, setGenError] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [viewed, setViewed] = useState<{ report: ReportEntry; markdown: string } | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const loadIndex = () => fetchReports().then(setIndex).catch(() => {})
  usePolling(loadIndex, 60000)
  usePolling(() => {
    fetchDigest(period).then(d => { setDigest(d); setDigestError(null) }).catch(e => setDigestError(errorMessage(e)))
  }, 300000, period)
  usePolling(() => {
    if (selected) fetchReport(selected).then(setViewed).catch(() => setViewed(null))
  }, null, selected)

  const sendDigest = async () => {
    setSending(true)
    setNotice(null)
    try {
      const res = await fetch('/api/reports/digest/send', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ period }) })
      if (!res.ok) throw new Error(await apiError(res))
      setNotice('Posted to the Webex space and saved in the history.')
      loadIndex()
    } catch (e) {
      setNotice(errorMessage(e))
    } finally {
      setSending(false)
    }
  }

  const copy = () => {
    if (!digest) return
    navigator.clipboard?.writeText(digest.markdown).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) }).catch(() => {})
  }

  const generate = async () => {
    setStreaming('')
    setGenError(null)
    setSelected(null)
    setViewed(null)
    try {
      const res = await fetch('/api/ai/report/generate', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ audience }) })
      if (!res.ok) throw new Error(await apiError(res))
      const reader = res.body?.getReader()
      if (!reader) throw new Error('Streaming is not supported by this browser')
      const decoder = new TextDecoder()
      let buffer = '', text = '', savedId = '', streamError = ''
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        let sep
        while ((sep = buffer.indexOf('\n\n')) !== -1) {
          const frame = buffer.slice(0, sep)
          buffer = buffer.slice(sep + 2)
          for (const line of frame.split('\n')) {
            if (!line.startsWith('data:')) continue
            try {
              const ev = JSON.parse(line.slice(5).trim())
              if (ev.type === 'text') { text += ev.text; setStreaming(text) }
              else if (ev.type === 'reset') { text = ''; setStreaming('') }
              else if (ev.type === 'saved') savedId = ev.text
              else if (ev.type === 'error') streamError = ev.text
            } catch { /* keep-alives */ }
          }
        }
      }
      if (streamError) throw new Error(streamError)
      if (!text.trim()) throw new Error('The AI provider returned an empty report. Check Settings → AI assistant and the operator logs.')
      await loadIndex()
      if (savedId) setSelected(savedId)
    } catch (e) {
      setGenError(errorMessage(e))
    } finally {
      setStreaming(null)
    }
  }

  const remove = async () => {
    if (!viewed) return
    const res = await fetch(`/api/reports/${encodeURIComponent(viewed.report.id)}`, { method: 'DELETE' })
    setConfirmDelete(false)
    if (res.ok) {
      setSelected(null)
      setViewed(null)
      loadIndex()
    }
  }

  const reports = index?.reports || []
  const shown = streaming !== null ? streaming : viewed?.markdown

  return (
    <div className="p-8 max-w-[1600px] mx-auto print-page">
      <div className="print-hide">
        <PageHeader title="Reports" subtitle="The cost digest teams and finance read, and AI-written reports for each audience" />
      </div>

      <div className="grid gap-4 lg:grid-cols-3 print-hide">
        <Card className="p-5 lg:col-span-2">
          <div className="flex flex-wrap items-center gap-3 mb-4">
            <div className="min-w-0">
              <h2 className="text-base font-semibold text-slate-800">{digest?.digest.title || 'Cost digest'}</h2>
              <p className="text-xs text-slate-500">Computed from the cost history, not written by a model, so everyone reads the same numbers.</p>
            </div>
            <div className="ml-auto flex flex-wrap items-center gap-2">
              <Tabs value={period} onChange={setPeriod} tabs={[{ id: 'week', label: 'Last 7 days' }, { id: 'month', label: 'Last month' }]} />
            </div>
          </div>
          {digestError ? <p className="text-sm text-rose-600">{digestError}</p>
            : !digest ? <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={14} className="animate-spin" /> Loading…</div>
            : <DigestView d={digest.digest} />}
          <div className="mt-5 pt-4 border-t border-slate-100 flex flex-wrap items-center gap-2">
            <Button size="sm" onClick={copy} disabled={!digest} icon={copied ? <Check size={14} /> : <Copy size={14} />}>{copied ? 'Copied' : 'Copy as Markdown'}</Button>
            <Button size="sm" onClick={() => digest && downloadText(`cost-digest-${digest.digest.to}.md`, digest.markdown)} disabled={!digest} icon={<Download size={14} />}>Download</Button>
            {can('operator') && (
              <span title={index?.webexReady ? undefined : 'Enable Webex with a space ID under Settings → Notifications'}>
                <Button size="sm" variant="primary" onClick={sendDigest} disabled={!digest || sending || !index?.webexReady} icon={sending ? <Loader2 size={14} className="animate-spin" /> : <Send size={14} />}>Send to Webex now</Button>
              </span>
            )}
            {notice && <span className="text-xs text-slate-600">{notice}</span>}
          </div>
        </Card>
        {index && <ScheduleCard index={index} onSaved={loadIndex} />}
      </div>

      <div className="mt-8 print-hide">
        <SectionTitle aside={<span className="text-xs text-slate-400">written by your AI model from the same numbers</span>}>AI reports</SectionTitle>
      </div>
      <div className="grid gap-4 lg:grid-cols-[20rem_1fr]">
        <div className="space-y-4 print-hide">
          {can('operator') && (
            <Card className="p-4">
              <div className="text-sm font-semibold text-slate-800 mb-2">New report</div>
              <div className="space-y-1.5">
                {audiences.map(a => (
                  <label key={a.id} className={`flex gap-2 p-2 rounded-lg border cursor-pointer ${audience === a.id ? 'border-brand-300 bg-brand-50/60' : 'border-slate-200 hover:bg-slate-50'}`}>
                    <input type="radio" name="audience" className="mt-1 accent-brand-600" checked={audience === a.id} onChange={() => setAudience(a.id)} />
                    <span><span className="block text-sm font-medium text-slate-800">{a.label}</span><span className="block text-xs text-slate-500">{a.hint}</span></span>
                  </label>
                ))}
              </div>
              <Button variant="primary" className="w-full mt-3 justify-center" onClick={generate} disabled={streaming !== null}
                icon={streaming !== null ? <Loader2 size={15} className="animate-spin" /> : <Sparkles size={15} />}>
                {streaming !== null ? 'Writing…' : 'Generate report'}
              </Button>
              {genError && <p className="mt-2 text-xs text-rose-600">{genError}</p>}
            </Card>
          )}
          <Card className="p-2">
            <div className="px-2 py-1.5 text-xs font-semibold text-slate-500">History</div>
            {reports.length === 0 ? <p className="px-2 pb-2 text-sm text-slate-400">No reports yet.</p> : (
              <div className="max-h-[32rem] overflow-y-auto">
                {reports.map(r => (
                  <button key={r.id} onClick={() => { setSelected(r.id); setConfirmDelete(false) }}
                    className={`w-full text-left px-2 py-2 rounded-lg ${selected === r.id ? 'bg-brand-50' : 'hover:bg-slate-50'}`}>
                    <div className="flex items-center gap-1.5">
                      {r.kind === 'digest' ? <CalendarClock size={13} className="text-slate-400" /> : <Sparkles size={13} className="text-brand-600" />}
                      <span className="text-sm font-medium text-slate-800 truncate">{r.title}</span>
                    </div>
                    <div className="mt-0.5 text-[11px] text-slate-500 pl-5">{when(r.generatedAt)}{r.by ? ` · ${r.by}` : ''}{r.delivered ? ` · sent to ${r.delivered}` : ''}</div>
                  </button>
                ))}
              </div>
            )}
          </Card>
        </div>

        <Card className="min-h-[28rem] print-container">
          {shown !== undefined && shown !== null ? (
            <>
              {viewed && streaming === null && (
                <div className="flex flex-wrap items-center gap-2 px-6 pt-5 print-hide">
                  <Badge tone={viewed.report.kind === 'ai' ? 'brand' : 'neutral'}>{viewed.report.kind === 'ai' ? audiences.find(a => a.id === viewed.report.audience)?.label || 'AI report' : 'Digest'}</Badge>
                  <span className="text-xs text-slate-500">{when(viewed.report.generatedAt)}</span>
                  <div className="ml-auto flex gap-2">
                    <Button size="sm" icon={<Download size={14} />} onClick={() => downloadText(`${viewed.report.id}.md`, viewed.markdown)}>Markdown</Button>
                    <Button size="sm" icon={<Printer size={14} />} onClick={() => window.print()}>PDF</Button>
                    {can('admin') && (confirmDelete
                      ? <Button size="sm" variant="danger" onClick={remove}>Delete for good</Button>
                      : <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} onClick={() => setConfirmDelete(true)}>Delete</Button>)}
                  </div>
                </div>
              )}
              <div className="prose prose-slate max-w-none p-8 print-prose prose-headings:tracking-tight prose-a:text-brand-700 prose-table:text-sm">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{shown || '…'}</ReactMarkdown>
              </div>
            </>
          ) : (
            <div className="h-full min-h-[28rem] flex flex-col items-center justify-center text-center p-8">
              <FileText size={44} className="text-slate-200 mb-3" />
              <p className="text-sm font-medium text-slate-600">Pick a report from the history, or generate a new one.</p>
              <p className="text-xs text-slate-400 mt-1">AI reports need a model under Settings → AI assistant; the digest above works without one.</p>
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}

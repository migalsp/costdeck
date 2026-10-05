import { useState, useEffect, useRef } from 'react'
import { usePolling } from '../lib/usePolling'
import { Cpu, Crown, Database, Download, RefreshCw, Terminal } from 'lucide-react'
import { Badge, Button, PageHeader } from '../components/ui'
import { KpiTile } from '../components/finops/charts'
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ReferenceLine,
  Legend
} from 'recharts'

// HealthSample is one reading from GET /api/operator/health.
interface HealthSample {
  status: string
  managedNamespaces: number
  memoryUsage: number
  cpuUsage: number
  memoryRequests: number
  memoryLimits: number
  cpuRequests: number
  cpuLimits: number
  goroutines: number
  cpuCores: number
  heapAllocMiB: number
  sysMemoryMiB: number
  gcCycles: number
  timestamp: string
  pod?: string
  leader?: string
  replicas?: { name: string; ready: boolean; node: string; restarts: number }[]
}

export default function OperatorHealth() {
  const [health, setHealth] = useState<HealthSample | null>(null)
  const [history, setHistory] = useState<(HealthSample & { time: string })[]>([])
  const [logs, setLogs] = useState<string>('')
  const [logPod, setLogPod] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [autoScroll, setAutoScroll] = useState(true)
  const logBoxRef = useRef<HTMLDivElement>(null)

  const fetchHealth = async () => {
    try {
      const res = await fetch('/api/operator/health')
      const data = await res.json()
      setHealth(data.current)
      setHistory(((data.history || []) as HealthSample[]).map(h => ({
        ...h,
        time: new Date(h.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
      })))
    } catch (err) {
      console.error("Failed to fetch health", err)
    }
  }

  const fetchLogs = async () => {
    try {
      const res = await fetch('/api/operator/logs')
      const data = await res.text()
      setLogs(data)
      setLogPod(res.headers.get('X-Costdeck-Pod') || '')
    } catch (err) {
      console.error("Failed to fetch logs", err)
    }
  }

  const refresh = () => Promise.all([fetchHealth(), fetchLogs()])

  // Background refreshes stay quiet; only the button spins.
  const handleRefresh = async () => {
    setRefreshing(true)
    await refresh()
    setRefreshing(false)
  }

  usePolling(() => { refresh().then(() => setLoading(false)) }, 5000)

  useEffect(() => {
    if (autoScroll) {
      // Scroll the log box only; scrollIntoView would drag the whole page down with it.
      const box = logBoxRef.current
      if (box) box.scrollTop = box.scrollHeight
    }
  }, [logs, autoScroll])

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-brand-500"></div>
      </div>
    )
  }

  const cpuReq = health?.cpuRequests || 0;
  const cpuLim = health?.cpuLimits || 0;
  const memReq = health?.memoryRequests || 0;
  const memLim = health?.memoryLimits || 0;

  // Detect errors in logs (look for ERROR level entries)
  const logLines = logs.split('\n');
  const errorCount = logLines.filter(line =>
    /\bERROR\b/i.test(line) || /"level":"error"/i.test(line)
  ).length;
  const hasErrors = errorCount > 0;

  return (
    <div className="p-8 max-w-[1200px] mx-auto">
      <PageHeader
        title="Cost Deck Health"
        subtitle="The operator itself: its replicas, resource use and logs"
        actions={<Button onClick={handleRefresh} disabled={refreshing} icon={<RefreshCw size={16} className={refreshing ? 'animate-spin' : ''} />}>Refresh</Button>}
      />

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-4">
        <KpiTile label="Status" value={hasErrors ? <span className="text-amber-700">{errorCount} error{errorCount > 1 ? 's' : ''}</span> : (health?.status || 'Unknown')}
          sub={hasErrors ? 'In the last log lines below' : 'No errors in the recent logs'} />
        <KpiTile label="Namespaces tracked" value={health?.managedNamespaces || 0} sub="With usage and cost history" />
        <KpiTile label="Goroutines" value={health?.goroutines || 0} sub={`${health?.cpuCores || 0} CPU cores visible to this pod`} />
        <KpiTile label="Memory" value={<>{health?.heapAllocMiB?.toFixed(0) || 0}<span className="text-sm font-medium text-slate-400"> MiB heap</span></>}
          sub={`${health?.sysMemoryMiB?.toFixed(0) || 0} MiB from the OS · ${health?.gcCycles || 0} GC cycles`} />
      </div>

      {health?.replicas && health.replicas.length > 0 && (
        <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-5 mb-8">
          <div className="flex items-baseline gap-3 mb-3">
            <h2 className="text-base font-semibold text-slate-800">Replicas</h2>
            <span className="text-xs text-slate-500">
              {health.replicas.length === 1 ? 'One replica: the dashboard is down while it restarts.' : 'Every replica serves the dashboard and API; the leader runs controllers, the Webex bot, cost history and the digest.'}
            </span>
          </div>
          <div className="divide-y divide-slate-100">
            {health.replicas.map(r => (
              <div key={r.name} className="flex flex-wrap items-center gap-2 py-2 text-sm">
                <span className={`w-2 h-2 rounded-full ${r.ready ? 'bg-brand-500' : 'bg-rose-500'}`} title={r.ready ? 'Ready' : 'Not ready'} />
                <span className="font-medium text-slate-800">{r.name}</span>
                {!r.ready && <Badge tone="danger">Not ready</Badge>}
                {r.name === health.leader && <Badge tone="brand"><Crown size={11} /> Leader</Badge>}
                {r.name === health.pod && <Badge>Serving this page</Badge>}
                <span className="ml-auto text-xs text-slate-500">{r.node || 'unscheduled'}{r.restarts ? ` · ${r.restarts} restart${r.restarts === 1 ? '' : 's'}` : ''}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Charts Section */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-8 mb-10">
        {/* CPU Chart */}
        <div className="bg-white p-6 rounded-xl border border-slate-200 shadow-sm flex flex-col">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2 text-slate-800">
              <div className="p-1.5 bg-brand-50 text-brand-600 rounded-lg"><Cpu size={16} /></div>
              <span className="font-bold text-sm">CPU Usage (Cores)</span>
            </div>
            {health && (
              <span className="text-[10px] font-bold text-slate-400 uppercase tracking-widest">
                {health.cpuUsage.toFixed(3)} / {cpuLim} Cores
              </span>
            )}
          </div>
          <div className="h-[260px] w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={history} margin={{ top: 10, right: 20, left: 10, bottom: 20 }}>
                <defs>
                  <linearGradient id="colorCpu" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#047857" stopOpacity={0.15} />
                    <stop offset="95%" stopColor="#047857" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="#f1f5f9" />
                <XAxis
                  dataKey="time"
                  fontSize={10}
                  tick={{ fill: '#94a3b8' }}
                  tickLine={false}
                  axisLine={{ stroke: '#e2e8f0' }}
                  label={{ value: 'Time', position: 'insideBottom', offset: -10, fontSize: 10, fill: '#94a3b8' }}
                />
                <YAxis
                  fontSize={10}
                  tick={{ fill: '#94a3b8' }}
                  tickLine={false}
                  axisLine={{ stroke: '#e2e8f0' }}
                  domain={[0, (dataMax: number) => Math.max(dataMax, cpuLim || 0.1) * 1.3]}
                  label={{ value: 'Cores', angle: -90, position: 'insideLeft', offset: 5, fontSize: 10, fill: '#94a3b8' }}
                />
                <Tooltip
                  contentStyle={{ borderRadius: '12px', border: 'none', boxShadow: '0 10px 15px -3px rgb(0 0 0 / 0.1)', fontSize: '12px' }}
                  formatter={(value) => [`${Number(value).toFixed(4)} cores`, 'CPU Usage']}
                />
                <Legend iconType="circle" wrapperStyle={{ fontSize: '10px', paddingTop: '8px' }} />
                <Area isAnimationActive={false} type="monotone" dataKey="cpuUsage" name="Usage" stroke="#047857" strokeWidth={2} fillOpacity={1} fill="url(#colorCpu)" dot={false} />
                {cpuReq > 0 && (
                  <ReferenceLine y={cpuReq} stroke="#64748b" strokeDasharray="6 3" strokeWidth={1.5}
                    label={{ position: 'right', value: `Req ${cpuReq}`, fill: '#64748b', fontSize: 9, fontWeight: 'bold' }} />
                )}
                {cpuLim > 0 && (
                  <ReferenceLine y={cpuLim} stroke="#94a3b8" strokeDasharray="6 3" strokeWidth={1.5}
                    label={{ position: 'right', value: `Lim ${cpuLim}`, fill: '#94a3b8', fontSize: 9, fontWeight: 'bold' }} />
                )}
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </div>

        {/* Memory Chart */}
        <div className="bg-white p-6 rounded-xl border border-slate-200 shadow-sm flex flex-col">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2 text-slate-800">
              <div className="p-1.5 bg-brand-50 text-brand-600 rounded-lg"><Database size={16} /></div>
              <span className="font-bold text-sm">RAM Memory (MiB)</span>
            </div>
            {health && (
              <span className="text-[10px] font-bold text-slate-400 uppercase tracking-widest">
                {Math.round(health.memoryUsage)} / {Math.round(memLim)} MiB
              </span>
            )}
          </div>
          <div className="h-[260px] w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={history} margin={{ top: 10, right: 20, left: 10, bottom: 20 }}>
                <defs>
                  <linearGradient id="colorMem" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#047857" stopOpacity={0.15} />
                    <stop offset="95%" stopColor="#047857" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="#f1f5f9" />
                <XAxis
                  dataKey="time"
                  fontSize={10}
                  tick={{ fill: '#94a3b8' }}
                  tickLine={false}
                  axisLine={{ stroke: '#e2e8f0' }}
                  label={{ value: 'Time', position: 'insideBottom', offset: -10, fontSize: 10, fill: '#94a3b8' }}
                />
                <YAxis
                  fontSize={10}
                  tick={{ fill: '#94a3b8' }}
                  tickLine={false}
                  axisLine={{ stroke: '#e2e8f0' }}
                  domain={[0, (dataMax: number) => Math.max(dataMax, memLim || 128) * 1.3]}
                  label={{ value: 'MiB', angle: -90, position: 'insideLeft', offset: 5, fontSize: 10, fill: '#94a3b8' }}
                />
                <Tooltip
                  contentStyle={{ borderRadius: '12px', border: 'none', boxShadow: '0 10px 15px -3px rgb(0 0 0 / 0.1)', fontSize: '12px' }}
                  formatter={(value) => [`${Number(value).toFixed(1)} MiB`, 'Memory Usage']}
                />
                <Legend iconType="circle" wrapperStyle={{ fontSize: '10px', paddingTop: '8px' }} />
                <Area isAnimationActive={false} type="monotone" dataKey="memoryUsage" name="Usage" stroke="#047857" strokeWidth={2} fillOpacity={1} fill="url(#colorMem)" dot={false} />
                {memReq > 0 && (
                  <ReferenceLine y={memReq} stroke="#64748b" strokeDasharray="6 3" strokeWidth={1.5}
                    label={{ position: 'right', value: `Req ${Math.round(memReq)}`, fill: '#64748b', fontSize: 9, fontWeight: 'bold' }} />
                )}
                {memLim > 0 && (
                  <ReferenceLine y={memLim} stroke="#94a3b8" strokeDasharray="6 3" strokeWidth={1.5}
                    label={{ position: 'right', value: `Lim ${Math.round(memLim)}`, fill: '#94a3b8', fontSize: 9, fontWeight: 'bold' }} />
                )}
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </div>
      </div>

      {/* Logs Section */}
      <div className="bg-slate-900 rounded-xl shadow-xl overflow-hidden flex flex-col h-[500px]">
        <div className="bg-slate-800 px-6 py-3 flex justify-between items-center border-b border-slate-700">
          <div className="flex items-center gap-2 text-slate-300">
            <Terminal size={14} />
            <span className="text-xs font-mono font-medium">Logs{logPod ? ` of ${logPod}` : ""} (last 100 lines)</span>
          </div>
          <div className="flex items-center gap-4">
            <label className="flex items-center gap-2 text-xs font-mono text-slate-400 cursor-pointer hover:text-white transition-colors">
              <input
                type="checkbox"
                checked={autoScroll}
                onChange={(e) => setAutoScroll(e.target.checked)}
                className="accent-slate-500 rounded-sm bg-slate-800 border-slate-600 w-3 h-3"
              />
              Auto-scroll
            </label>
            <a
              href="/api/operator/logs/download"
              className="flex items-center gap-2 px-3 py-1 bg-slate-700 hover:bg-slate-600 text-white rounded text-xs transition-colors"
            >
              <Download size={12} />
              Download full logs
            </a>
          </div>
        </div>
        <div ref={logBoxRef} className="flex-1 overflow-auto p-6 font-mono text-[13px] text-slate-300 whitespace-pre">
          {logs || 'No logs available...'}
        </div>
      </div>
    </div>
  )
}

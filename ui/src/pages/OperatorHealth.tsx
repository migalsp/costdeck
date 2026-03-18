import { useState, useEffect, useRef } from 'react'
import { Activity, AlertTriangle, Shield, Cpu, Database, Download, RefreshCw, Terminal, Box, Zap, Recycle } from 'lucide-react'
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

const HealthCard = ({ icon, title, value, subtitle, variant = 'default' }: { icon: React.ReactNode, title: string, value: string | number, subtitle: string, variant?: 'default' | 'warning' }) => (
  <div className={`p-6 rounded-xl border shadow-sm ${
    variant === 'warning' 
      ? 'bg-amber-50 border-amber-200' 
      : 'bg-white border-slate-200'
  }`}>
    <div className="flex items-center gap-3 mb-4">
      {icon}
      <span className="font-semibold uppercase tracking-wider text-xs text-slate-500">{title}</span>
    </div>
    <p className={`text-2xl font-bold ${
      variant === 'warning' ? 'text-amber-600' : 'text-slate-800'
    }`}>{value}</p>
    <p className={`text-[10px] font-medium mt-1 uppercase tracking-tight ${
      variant === 'warning' ? 'text-amber-500' : 'text-slate-400'
    }`}>{subtitle}</p>
  </div>
);

export default function OperatorHealth() {
  const [health, setHealth] = useState<any | null>(null)
  const [history, setHistory] = useState<any[]>([])
  const [logs, setLogs] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [autoScroll, setAutoScroll] = useState(true)
  const logEndRef = useRef<HTMLDivElement>(null)

  const fetchHealth = async () => {
    try {
      const res = await fetch('/api/operator/health')
      const data = await res.json()
      setHealth(data.current)
      setHistory((data.history || []).map((h: any) => ({
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
    } catch (err) {
      console.error("Failed to fetch logs", err)
    }
  }

  const handleRefresh = async () => {
    setRefreshing(true)
    await Promise.all([fetchHealth(), fetchLogs()])
    setRefreshing(false)
  }

  useEffect(() => {
    const init = async () => {
      await handleRefresh()
      setLoading(false)
    }
    init()
    const interval = setInterval(handleRefresh, 5000)
    return () => clearInterval(interval)
  }, [])

  useEffect(() => {
    if (autoScroll) {
      logEndRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [logs, autoScroll])

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-emerald-500"></div>
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
    <div className="p-8 max-w-[1200px] mx-auto animate-in fade-in duration-500">
      <div className="flex justify-between items-center mb-8">
        <div>
          <h2 className="text-3xl font-bold tracking-tight text-slate-800 flex items-center gap-3">
            <Shield className="text-emerald-500" size={32} />
            Cost Deck Health
          </h2>
          <p className="text-slate-500 mt-1">Real-time health metrics and internal system logs</p>
        </div>
        <button
          onClick={handleRefresh}
          className={`flex items-center gap-2 px-4 py-2 bg-white border border-slate-200 rounded-lg shadow-sm hover:bg-slate-50 transition-all ${refreshing ? 'text-slate-400 cursor-not-allowed' : 'text-slate-700'}`}
        >
          <RefreshCw size={18} className={refreshing ? 'animate-spin' : ''} />
          Refresh
        </button>
      </div>

      {/* Top Cards — Go Runtime Metrics */}
      <div className="grid grid-cols-1 md:grid-cols-4 gap-6 mb-8">
        <HealthCard 
          icon={hasErrors ? <AlertTriangle className="text-amber-500" /> : <Activity className="text-emerald-500" />} 
          title="Status" 
          value={hasErrors ? `${errorCount} error${errorCount > 1 ? 's' : ''}` : (health?.status || 'Unknown')} 
          subtitle={hasErrors ? 'Check operator logs below' : 'Operator is running'}
          variant={hasErrors ? 'warning' : 'default'}
        />
        <HealthCard icon={<Box className="text-blue-500" />} title="Managed" value={health?.managedNamespaces || 0} subtitle="Active namespaces" />
        <HealthCard icon={<Zap className="text-amber-500" />} title="Goroutines" value={health?.goroutines || 0} subtitle={`${health?.cpuCores || 0} CPU cores available`} />
        <HealthCard icon={<Recycle className="text-violet-500" />} title="GC Cycles" value={health?.gcCycles || 0} subtitle={`Heap: ${health?.heapAllocMiB?.toFixed(1) || '0'} MiB · Sys: ${health?.sysMemoryMiB?.toFixed(0) || '0'} MiB`} />
      </div>

      {/* Charts Section */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-8 mb-10">
        {/* CPU Chart */}
        <div className="bg-white p-6 rounded-2xl border border-slate-200 shadow-sm flex flex-col">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2 text-slate-800">
              <div className="p-1.5 bg-indigo-50 text-indigo-500 rounded-lg"><Cpu size={16} /></div>
              <span className="font-bold text-sm">CPU Usage (Cores)</span>
            </div>
            {health && (
              <span className="text-[10px] font-black text-slate-400 uppercase tracking-widest">
                {health.cpuUsage.toFixed(3)} / {cpuLim} Cores
              </span>
            )}
          </div>
          <div className="h-[260px] w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={history} margin={{ top: 10, right: 20, left: 10, bottom: 20 }}>
                <defs>
                  <linearGradient id="colorCpu" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#6366f1" stopOpacity={0.15}/>
                    <stop offset="95%" stopColor="#6366f1" stopOpacity={0}/>
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
                  domain={[0, (dataMax: any) => Math.max(dataMax, cpuLim || 0.1) * 1.3]}
                  label={{ value: 'Cores', angle: -90, position: 'insideLeft', offset: 5, fontSize: 10, fill: '#94a3b8' }}
                />
                <Tooltip
                  contentStyle={{ borderRadius: '12px', border: 'none', boxShadow: '0 10px 15px -3px rgb(0 0 0 / 0.1)', fontSize: '12px' }}
                  formatter={(value: any) => [`${Number(value).toFixed(4)} cores`, 'CPU Usage']}
                />
                <Legend iconType="circle" wrapperStyle={{ fontSize: '10px', paddingTop: '8px' }} />
                <Area type="monotone" dataKey="cpuUsage" name="Usage" stroke="#6366f1" strokeWidth={2} fillOpacity={1} fill="url(#colorCpu)" dot={false} />
                {cpuReq > 0 && (
                  <ReferenceLine y={cpuReq} stroke="#6366f1" strokeDasharray="6 3" strokeWidth={1.5}
                    label={{ position: 'right', value: `Req ${cpuReq}`, fill: '#6366f1', fontSize: 9, fontWeight: 'bold' }} />
                )}
                {cpuLim > 0 && (
                  <ReferenceLine y={cpuLim} stroke="#f43f5e" strokeDasharray="6 3" strokeWidth={1.5}
                    label={{ position: 'right', value: `Lim ${cpuLim}`, fill: '#f43f5e', fontSize: 9, fontWeight: 'bold' }} />
                )}
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </div>

        {/* Memory Chart */}
        <div className="bg-white p-6 rounded-2xl border border-slate-200 shadow-sm flex flex-col">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2 text-slate-800">
              <div className="p-1.5 bg-emerald-50 text-emerald-500 rounded-lg"><Database size={16} /></div>
              <span className="font-bold text-sm">RAM Memory (MiB)</span>
            </div>
            {health && (
              <span className="text-[10px] font-black text-slate-400 uppercase tracking-widest">
                {Math.round(health.memoryUsage)} / {Math.round(memLim)} MiB
              </span>
            )}
          </div>
          <div className="h-[260px] w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={history} margin={{ top: 10, right: 20, left: 10, bottom: 20 }}>
                <defs>
                  <linearGradient id="colorMem" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#10b981" stopOpacity={0.15}/>
                    <stop offset="95%" stopColor="#10b981" stopOpacity={0}/>
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
                  domain={[0, (dataMax: any) => Math.max(dataMax, memLim || 128) * 1.3]}
                  label={{ value: 'MiB', angle: -90, position: 'insideLeft', offset: 5, fontSize: 10, fill: '#94a3b8' }}
                />
                <Tooltip 
                  contentStyle={{ borderRadius: '12px', border: 'none', boxShadow: '0 10px 15px -3px rgb(0 0 0 / 0.1)', fontSize: '12px' }}
                  formatter={(value: any) => [`${Number(value).toFixed(1)} MiB`, 'Memory Usage']}
                />
                <Legend iconType="circle" wrapperStyle={{ fontSize: '10px', paddingTop: '8px' }} />
                <Area type="monotone" dataKey="memoryUsage" name="Usage" stroke="#10b981" strokeWidth={2} fillOpacity={1} fill="url(#colorMem)" dot={false} />
                {memReq > 0 && (
                  <ReferenceLine y={memReq} stroke="#10b981" strokeDasharray="6 3" strokeWidth={1.5}
                    label={{ position: 'right', value: `Req ${Math.round(memReq)}`, fill: '#10b981', fontSize: 9, fontWeight: 'bold' }} />
                )}
                {memLim > 0 && (
                  <ReferenceLine y={memLim} stroke="#f43f5e" strokeDasharray="6 3" strokeWidth={1.5}
                    label={{ position: 'right', value: `Lim ${Math.round(memLim)}`, fill: '#f43f5e', fontSize: 9, fontWeight: 'bold' }} />
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
            <span className="text-xs font-mono font-medium">Internal Operator Logs (Trailing 100)</span>
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
        <div className="flex-1 overflow-auto p-6 font-mono text-[13px] text-slate-300 whitespace-pre">
          {logs || 'No logs available...'}
          <div ref={logEndRef} />
        </div>
      </div>
    </div>
  )
}

import { useState, useEffect } from 'react'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  ReferenceLine
} from 'recharts'
import { AlertTriangle, CheckCircle, Database, Cpu, Zap, RotateCcw } from 'lucide-react'

interface NamespaceCardProps {
  namespace: string;
  insights?: string[];
  onClick?: () => void;
}

// Convert "100m" to 0.1, "1" to 1.0
// Convert "512Mi" to 512, "1Gi" to 1024
const parseCpu = (v: string): number => {
  if (!v) return 0;
  if (v.endsWith('n')) return parseInt(v.slice(0, -1), 10) / 1000000000;
  if (v.endsWith('u')) return parseInt(v.slice(0, -1), 10) / 1000000;
  if (v.endsWith('m')) return parseInt(v.slice(0, -1), 10) / 1000;
  return parseFloat(v) || 0;
}

const parseMem = (v: string): number => {
  if (!v) return 0;
  if (v.endsWith('ki') || v.endsWith('Ki')) return parseInt(v.slice(0, -2)) / 1024;
  if (v.endsWith('mi') || v.endsWith('Mi')) return parseInt(v.slice(0, -2));
  if (v.endsWith('gi') || v.endsWith('Gi')) return parseInt(v.slice(0, -2)) * 1024;
  // If it's just raw bytes
  return parseInt(v) / (1024 * 1024) || 0;
}

export default function NamespaceCard({ namespace, insights = [], onClick }: NamespaceCardProps) {
  const [history, setHistory] = useState<any[]>([])
  const [optimization, setOptimization] = useState<any>(null)
  const [loading, setLoading] = useState(true)
  const [actionLoading, setActionLoading] = useState<'optimize' | 'revert' | null>(null)

  const fetchOptimization = () => {
    fetch(`/api/namespaces/${namespace}/optimization`)
      .then(res => res.json())
      .then(data => setOptimization(data))
      .catch(err => console.error("Failed to fetch optimization", err))
  }

  const fetchData = () => {
    fetch(`/api/namespaces/${namespace}/history`)
      .then(res => res.json())
      .then(data => {
        const formattedData = (data || []).map((point: any) => {
          const t = new Date(point.timestamp)
          return {
            time: `${t.getHours().toString().padStart(2, '0')}:${t.getMinutes().toString().padStart(2, '0')}`,
            cpuUsage: parseCpu(point.cpu?.usage),
            cpuReq: parseCpu(point.cpu?.requests),
            cpuLim: parseCpu(point.cpu?.limits),
            memUsage: parseMem(point.memory?.usage),
            memReq: parseMem(point.memory?.requests),
            memLim: parseMem(point.memory?.limits),
          }
        })
        setHistory(formattedData)
      })
      .catch(err => {
        console.error("Failed to load history for", namespace, err)
      })
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    fetchData()
    fetchOptimization()
    // Auto-refresh every 30 seconds
    const interval = setInterval(() => {
      fetchData()
      fetchOptimization()
    }, 30000)
    return () => clearInterval(interval)
  }, [namespace])

  const handleOptimize = async (e: React.MouseEvent) => {
    e.stopPropagation()
    if (!window.confirm(`Optimize ${namespace}? This will adjust requests/limits based on 1h average usage (+30%/50% margin).`)) return
    
    setActionLoading('optimize')
    // Optimistically add an "Optimizing" tag to insights if we want, but the spinner is usually enough
    try {
      const res = await fetch(`/api/namespaces/${namespace}/optimize`, { method: 'POST' })
      if (!res.ok) throw new Error('Optimization failed')
      
      fetchOptimization()
      fetchData()
      
      let attempts = 0;
      const fastPoll = setInterval(() => {
        fetchOptimization()
        fetchData()
        attempts++;
        if (attempts > 5) {
          clearInterval(fastPoll);
          setActionLoading(null)
        }
      }, 2000);
      setTimeout(() => setActionLoading(null), 12000);

    } catch (err) {
      alert("Failed to optimize: " + err)
      setActionLoading(null)
    }
  }

  const handleRevert = async (e: React.MouseEvent) => {
    e.stopPropagation()
    if (!window.confirm(`Revert optimization for ${namespace}? This will restore the original resource values.`)) return

    setActionLoading('revert')
    try {
      const res = await fetch(`/api/namespaces/${namespace}/revert`, { method: 'POST' })
      if (!res.ok) throw new Error('Revert failed')
      
      fetchOptimization()
      fetchData()
      
      let attempts = 0;
      const fastPoll = setInterval(() => {
        fetchOptimization()
        fetchData()
        attempts++;
        if (attempts > 5) {
          clearInterval(fastPoll);
          setActionLoading(null)
        }
      }, 2000);
      setTimeout(() => setActionLoading(null), 12000);

    } catch (err) {
      alert("Failed to revert: " + err)
      setActionLoading(null)
    }
  }

  const latest = history[history.length - 1] || {
    cpuUsage: 0, cpuReq: 0, cpuLim: 0, memUsage: 0, memReq: 0, memLim: 0
  }

  // Calculate ratios for the stats text
  const cpuProvisionRatio = latest.cpuReq > 0 ? (latest.cpuUsage / latest.cpuReq) * 100 : 0
  const memProvisionRatio = latest.memReq > 0 ? (latest.memUsage / latest.memReq) * 100 : 0

  return (
    <div 
      onClick={onClick}
      className="bg-white rounded-xl shadow-[0_2px_10px_-3px_rgba(6,81,237,0.1)] border border-slate-100 overflow-hidden flex flex-col cursor-pointer hover:border-emerald-500/50 hover:shadow-md transition-all group"
    >
      {/* Header */}
      <div className="px-6 py-4 border-b border-slate-100 flex justify-between items-center bg-white">
        <h3 className="font-semibold text-lg text-slate-800 flex items-center gap-2">
          {namespace}
        </h3>
        
        <div className="flex flex-wrap gap-2">
          {insights.length > 0 ? (
            insights.map(tag => (
              <div 
                key={tag}
                className={`flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-medium border ${
                  tag === 'Optimized' 
                    ? 'bg-emerald-50 text-emerald-600 border-emerald-200' 
                    : tag.includes('Missing') || tag.includes('Uncapped')
                    ? 'bg-red-50 text-red-600 border-red-200'
                    : 'bg-amber-50 text-amber-600 border-amber-200'
                }`}
              >
                {tag === 'Optimized' ? <CheckCircle size={14} /> : <AlertTriangle size={14} />}
                <span>{tag}</span>
              </div>
            ))
          ) : (
            <div className="flex items-center gap-1.5 px-3 py-1 rounded-full bg-slate-50 text-slate-500 border border-slate-200 text-xs font-medium">
               <span>Collecting data...</span>
            </div>
          )}
        </div>
      </div>

      <div className="p-6 grid grid-cols-1 md:grid-cols-2 gap-8 flex-1">
        
        {/* CPU CHART */}
        <div className="flex flex-col">
          <div className="flex justify-between items-end mb-4">
            <div className="flex items-center gap-2 text-slate-700">
               <div className="p-1.5 bg-blue-50 text-blue-500 rounded-md">
                 <Cpu size={18} />
               </div>
               <span className="font-medium">CPU (Cores)</span>
            </div>
            <div className="text-right">
              <span className="text-2xl font-bold text-slate-800">{latest.cpuUsage.toFixed(2)}</span>
              <span className="text-sm font-medium text-slate-400 ml-1">/ {latest.cpuReq.toFixed(1)} Requests</span>
            </div>
          </div>
          
          <div className="h-[180px] w-full mt-auto">
            {loading ? (
               <div className="h-full w-full bg-slate-50 animate-pulse rounded-lg"></div>
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={history} margin={{ top: 10, right: 0, left: -20, bottom: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="#E2E8F0" />
                  <XAxis dataKey="time" tick={{fontSize: 10, fill: '#94a3b8'}} tickLine={false} axisLine={false} minTickGap={20} />
                  <YAxis 
                    tick={{fontSize: 10, fill: '#94a3b8'}} 
                    tickLine={false} 
                    axisLine={false} 
                    domain={[0, 'auto']}
                    tickFormatter={(val) => val < 1 ? val.toFixed(3) : val.toFixed(2)}
                  />
                  <Tooltip 
                    contentStyle={{ borderRadius: '8px', border: 'none', boxShadow: '0 4px 6px -1px rgb(0 0 0 / 0.1)' }}
                    itemStyle={{ fontSize: '12px', fontWeight: 500 }}
                    formatter={(value: any) => [`${parseFloat(value).toFixed(3)} Cores`, 'Usage']}
                  />
                  {/* Real Usage (Blue) */}
                  <Area type="monotone" dataKey="cpuUsage" name="Usage" stroke="#3b82f6" fillOpacity={1} fill="url(#colorCpuUsage)" />
                  {/* Requests (Green Line) and Limit (Red Line) */}
                  {latest.cpuReq > 0 && (
                    <ReferenceLine y={latest.cpuReq} stroke="#10b981" strokeDasharray="3 3" label={{position: 'insideTopLeft', value: `Req: ${latest.cpuReq.toFixed(2)}`, fill: '#10b981', fontSize: 10}} />
                  )}
                  {latest.cpuLim > 0 && (
                    <ReferenceLine y={latest.cpuLim} stroke="#ef4444" strokeDasharray="3 3" label={{position: 'insideTopLeft', value: `Lim: ${latest.cpuLim.toFixed(2)}`, fill: '#ef4444', fontSize: 10}} />
                  )}
                  
                  <defs>
                    <linearGradient id="colorCpuUsage" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#3b82f6" stopOpacity={0.2}/>
                      <stop offset="95%" stopColor="#3b82f6" stopOpacity={0}/>
                    </linearGradient>
                  </defs>
                </AreaChart>
              </ResponsiveContainer>
            )}
          </div>
          <div className="mt-3 text-[11px] text-slate-400 text-center">
            Last 60 Minutes History
          </div>
        </div>

        {/* RAM CHART */}
        <div className="flex flex-col">
          <div className="flex justify-between items-end mb-4">
            <div className="flex items-center gap-2 text-slate-700">
               <div className="p-1.5 bg-indigo-50 text-indigo-500 rounded-md">
                 <Database size={18} />
               </div>
               <span className="font-medium">Memory (MiB)</span>
            </div>
            <div className="text-right">
              <span className="text-2xl font-bold text-slate-800">{latest.memUsage.toFixed(0)}</span>
              <span className="text-sm font-medium text-slate-400 ml-1">/ {latest.memReq.toFixed(0)} Requests</span>
            </div>
          </div>
          
          <div className="h-[180px] w-full mt-auto">
            {loading ? (
               <div className="h-full w-full bg-slate-50 animate-pulse rounded-lg"></div>
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={history} margin={{ top: 10, right: 0, left: -10, bottom: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="#E2E8F0" />
                  <XAxis dataKey="time" tick={{fontSize: 10, fill: '#94a3b8'}} tickLine={false} axisLine={false} minTickGap={20} />
                  <YAxis tick={{fontSize: 10, fill: '#94a3b8'}} tickLine={false} axisLine={false} domain={[0, 'auto']} />
                  <Tooltip 
                    contentStyle={{ borderRadius: '8px', border: 'none', boxShadow: '0 4px 6px -1px rgb(0 0 0 / 0.1)' }}
                    itemStyle={{ fontSize: '12px', fontWeight: 500 }}
                    formatter={(value: any) => [`${parseFloat(value).toFixed(1)} MiB`, 'Usage']}
                  />
                  {/* Real Usage (Blue) */}
                  <Area type="monotone" dataKey="memUsage" name="Usage (MiB)" stroke="#6366f1" fillOpacity={1} fill="url(#colorMemUsage)" />
                  {/* Requests (Green Line) and Limit (Red Line) */}
                  {latest.memReq > 0 && (
                    <ReferenceLine y={latest.memReq} stroke="#10b981" strokeDasharray="3 3" label={{position: 'insideTopLeft', value: `Req: ${latest.memReq}`, fill: '#10b981', fontSize: 10}} />
                  )}
                  {latest.memLim > 0 && (
                    <ReferenceLine y={latest.memLim} stroke="#ef4444" strokeDasharray="3 3" label={{position: 'insideTopLeft', value: `Lim: ${latest.memLim}`, fill: '#ef4444', fontSize: 10}} />
                  )}
                  
                  <defs>
                    <linearGradient id="colorMemUsage" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#6366f1" stopOpacity={0.2}/>
                      <stop offset="95%" stopColor="#6366f1" stopOpacity={0}/>
                    </linearGradient>
                  </defs>
                </AreaChart>
              </ResponsiveContainer>
            )}
          </div>
          <div className="mt-3 text-[11px] text-slate-400 text-center">
            Last 60 Minutes History
          </div>
        </div>

      </div>
      
      {/* Footer FinOps text stats */}
      <div className="bg-slate-50 px-6 py-3 border-t border-slate-100 flex justify-between items-center text-xs text-slate-600">
        <div className="flex gap-6">
          <div className="flex gap-2">
            <span className="font-semibold text-slate-800">CPU Usage:</span>
            <span>{latest.cpuUsage.toFixed(3)} Cores ({cpuProvisionRatio.toFixed(1)}% of Requests)</span>
          </div>
          <div className="flex gap-2">
            <span className="font-semibold text-slate-800">RAM Usage:</span>
            <span>{latest.memUsage.toFixed(1)} MiB ({memProvisionRatio.toFixed(1)}% of Requests)</span>
          </div>
        </div>

        <div className="flex gap-2">
          {(optimization?.active && actionLoading !== 'optimize') || actionLoading === 'revert' ? (
            <button 
              onClick={handleRevert}
              disabled={actionLoading !== null}
              className={`flex items-center gap-1.5 px-3 py-1.5 bg-amber-50 text-amber-600 border border-amber-200 rounded-lg hover:bg-amber-100 transition-colors font-bold uppercase tracking-tight ${actionLoading !== null ? 'opacity-60 cursor-not-allowed' : ''}`}
            >
              {actionLoading === 'revert' ? (
                 <div className="w-3.5 h-3.5 border-2 border-amber-200 border-t-amber-500 rounded-full animate-spin"></div>
              ) : (
                <RotateCcw size={14} />
              )}
              {actionLoading === 'revert' ? 'Reverting...' : 'Revert'}
            </button>
          ) : (
            (insights.some(i => i.includes('Overprovisioned')) || actionLoading === 'optimize') && (
              <button 
                onClick={handleOptimize}
                disabled={actionLoading !== null}
                className={`flex items-center gap-1.5 px-3 py-1.5 bg-emerald-50 text-emerald-600 border border-emerald-200 rounded-lg hover:bg-emerald-100 transition-colors font-bold uppercase tracking-tight ${actionLoading !== null ? 'opacity-60 cursor-not-allowed' : ''}`}
              >
                {actionLoading === 'optimize' ? (
                   <div className="w-3.5 h-3.5 border-2 border-emerald-200 border-t-emerald-500 rounded-full animate-spin"></div>
                ) : (
                  <Zap size={14} fill="currentColor" />
                )}
                {actionLoading === 'optimize' ? 'Optimizing...' : 'Optimize'}
              </button>
            )
          )}
        </div>
      </div>
    </div>
  )
}

import { useState, useEffect } from 'react'
import { ArrowLeft, Search, Activity, AlertCircle, Play, Square, Settings2, Clock, Plus } from 'lucide-react'
import ScalingConfigModal from '../components/ScalingConfigModal'
import InfoTooltip from '../components/InfoTooltip'

interface PodDetail {
  name: string;
  status: string;
  cpu: {
    usage: string;
    requests: string;
    limits: string;
  };
  memory: {
    usage: string;
    requests: string;
    limits: string;
  };
}

interface NamespaceDetailsProps {
  namespace: string;
  onBack: () => void;
}

const formatCpu = (v: string): string => {
  if (!v || v === '0') return '0';
  if (v.endsWith('n')) return (parseInt(v.slice(0, -1), 10) / 1000000000).toFixed(3);
  if (v.endsWith('u')) return (parseInt(v.slice(0, -1), 10) / 1000000).toFixed(3);
  if (v.endsWith('m')) return (parseInt(v.slice(0, -1), 10) / 1000).toFixed(3);
  return parseFloat(v).toFixed(3);
}

const formatMem = (v: string): string => {
  if (!v || v === '0') return '0';
  let bytes = 0;
  const val = v.toLowerCase();
  if (val.endsWith('ki')) bytes = parseInt(v) * 1024;
  else if (val.endsWith('mi')) bytes = parseInt(v) * 1024 * 1024;
  else if (val.endsWith('gi')) bytes = parseInt(v) * 1024 * 1024 * 1024;
  else bytes = parseInt(v);
  
  return (bytes / (1024 * 1024)).toFixed(1) + ' MiB';
}

export default function NamespaceDetails({ namespace, onBack }: NamespaceDetailsProps) {
  const [pods, setPods] = useState<PodDetail[]>([])
  const [optimization, setOptimization] = useState<any>(null)
  const [loading, setLoading] = useState(true)
  const [filterQuery, setFilterQuery] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [config, setConfig] = useState<any>(null)
  const [isEditingConfig, setIsEditingConfig] = useState(false)
  const [sortField, setSortField] = useState<keyof PodDetail | 'cpuUsage' | 'memUsage' | 'cpuReq' | 'cpuLim' | 'memReq' | 'memLim'>('name')
  const [sortDirection, setSortDirection] = useState<'asc' | 'desc'>('asc')
  const [isScaling, setIsScaling] = useState(false)

  const fetchPods = (silent = false) => {
    if (!silent) setLoading(true);
    fetch(`/api/namespaces/${namespace}/pods`)
      .then(res => res.json())
      .then(data => {
        setPods(data || []);
        setError(null);
      })
      .catch(err => {
        console.error(err);
        if (!silent) setError("Failed to load pod details.");
      })
      .finally(() => {
        if (!silent) setLoading(false);
      });
  }

  const fetchConfig = () => {
    fetch('/api/scaling/configs')
      .then(res => res.json())
      .then(data => {
        const p = (data || []).find((p: any) => p.spec.targetNamespace === namespace);
        setConfig(p);
      })
      .catch(console.error);
  }

  const fetchOptimization = () => {
    fetch(`/api/namespaces/${namespace}/optimization`)
      .then(res => res.json())
      .then(data => setOptimization(data))
      .catch(console.error);
  }

  useEffect(() => {
    fetchPods()
    fetchConfig()
    fetchOptimization()
    const interval = setInterval(() => {
      fetchPods(true)
      fetchConfig()
      fetchOptimization()
    }, 10000)
    return () => clearInterval(interval)
  }, [namespace])

  const handleManualScale = async (active: boolean) => {
    if (!config || isScaling) return;
    setIsScaling(true);
    
    // Optimistic UI Update
    const previousPhase = config.status?.phase;
    setConfig({
      ...config,
      status: {
        ...config.status,
        phase: 'Scaling...'
      }
    });

    try {
      await fetch(`/api/scaling/configs/${config.metadata.name}/manual`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ active })
      });
      fetchConfig();
      setError(null);
      
      // Fast polling setup to catch the result quickly
      let attempts = 0;
      const fastPoll = setInterval(() => {
        fetchPods(true);
        fetchConfig();
        attempts++;
        if (attempts > 10) { // Keep fast polling for ~20 seconds maximum
          clearInterval(fastPoll);
          setIsScaling(false);
        }
      }, 2000);

      // We clear the isScaling explicitly when the phase actually changes from 'Scaling...' inside fetchConfig,
      // but we need a fallback timeout here just in case.
      setTimeout(() => setIsScaling(false), 22000);

    } catch (err) {
      console.error("Error during manual scale:", err);
      setError("Failed to apply manual scaling. Please check the console for details.");
      // Revert optimistic update on failure
      setConfig({
        ...config,
        status: { ...config.status, phase: previousPhase }
      });
      setIsScaling(false);
    }
  };

  const handleCreateConfig = async () => {
    try {
      await fetch('/api/scaling/configs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          metadata: { name: `config-${namespace}` },
          spec: { targetNamespace: namespace, active: true }
        })
      });
      fetchConfig();
    } catch (err) {
      console.error(err);
    }
  };

  const handleUpdateConfig = async (updatedSpec: any) => {
    if (!config) return;
    try {
      const res = await fetch(`/api/scaling/configs/${config.metadata.name}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ 
          metadata: { name: config.metadata.name },
          spec: updatedSpec 
        })
      });

      if (!res.ok) {
        const text = await res.text();
        throw new Error(text || res.statusText);
      }

      setIsEditingConfig(false);
      setError(null);
      fetchConfig();
    } catch (err: any) {
      console.error(err);
      setError(`Failed to update configuration: ${err.message}`);
    }
  };

  const handleSort = (field: typeof sortField) => {
    if (sortField === field) {
      setSortDirection(sortDirection === 'asc' ? 'desc' : 'asc')
    } else {
      setSortField(field)
      setSortDirection('asc')
    }
  }

  const getSortedPods = () => {
    return [...pods].sort((a, b) => {
      let valA: any = a[sortField as keyof PodDetail] || '';
      let valB: any = b[sortField as keyof PodDetail] || '';
      
      // Handle nested metrics for sorting
      if (sortField === 'cpuUsage') {
        valA = parseFloat(formatCpu(a.cpu.usage));
        valB = parseFloat(formatCpu(b.cpu.usage));
      } else if (sortField === 'memUsage') {
        valA = parseFloat(formatMem(a.memory.usage));
        valB = parseFloat(formatMem(b.memory.usage));
      } else if (sortField === 'cpuReq') {
        valA = parseFloat(formatCpu(a.cpu.requests));
        valB = parseFloat(formatCpu(b.cpu.requests));
      } else if (sortField === 'cpuLim') {
        valA = parseFloat(formatCpu(a.cpu.limits));
        valB = parseFloat(formatCpu(b.cpu.limits));
      } else if (sortField === 'memReq') {
        valA = parseFloat(formatMem(a.memory.requests));
        valB = parseFloat(formatMem(b.memory.requests));
      } else if (sortField === 'memLim') {
        valA = parseFloat(formatMem(a.memory.limits));
        valB = parseFloat(formatMem(b.memory.limits));
      }

      if (valA < valB) return sortDirection === 'asc' ? -1 : 1;
      if (valA > valB) return sortDirection === 'asc' ? 1 : -1;
      return 0;
    })
  }

  const filteredPods = getSortedPods().filter(pod => 
    pod.name.toLowerCase().includes(filterQuery.toLowerCase())
  )

  const getOptimizationForPod = (podName: string) => {
    if (!optimization?.active || !optimization.workloads) return null;
    return optimization.workloads.find((w: any) => podName.startsWith(w.name));
  }

  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-8">
        <button 
          onClick={onBack}
          className="p-2 bg-white border border-slate-200 rounded-lg shadow-sm hover:bg-slate-50 transition-colors"
        >
          <ArrowLeft size={20} className="text-slate-600" />
        </button>
        <div className="flex items-center gap-2">
          <div>
            <h2 className="text-3xl font-black tracking-tight text-slate-900 uppercase">Namespace Insight: {namespace}</h2>
            <p className="text-slate-500 mt-1 font-medium italic">Detailed resource analytics and scaling management</p>
          </div>
          <InfoTooltip content="This view shows real-time metrics for each pod. Strike-through values indicate optimized resources. Green values are currently active." position="bottom" />
        </div>
      </div>

      {/* Scaling Controls Section */}
      <div className="bg-slate-900 rounded-2xl p-6 mb-8 text-white shadow-xl flex items-center justify-between border border-white/10 overflow-hidden relative">
        <div className="absolute top-0 right-0 p-8 opacity-10 pointer-events-none">
          <Activity size={120} />
        </div>
        <div className="relative z-10 flex items-center gap-6">
          <div className={`p-4 rounded-2xl ${config ? 'bg-indigo-500/20 text-indigo-400' : 'bg-slate-800 text-slate-500'}`}>
            <Clock size={32} />
          </div>
          <div>
            <h3 className="text-xl font-bold flex items-center gap-2">
              Scaling Status
              {config && (
                <span className={`px-2 py-0.5 rounded text-[10px] uppercase tracking-widest ${
                  config.status?.phase === 'ScaledUp' ? 'bg-emerald-500/20 text-emerald-400' : 
                  config.status?.phase === 'Scaling...' ? 'bg-blue-500/20 text-blue-400 animate-pulse' :
                  'bg-amber-500/20 text-amber-400'
                }`}>
                  {config.status?.phase || 'Idle'}
                </span>
              )}
            </h3>
            <p className="text-slate-400 text-sm mt-1">
              {config 
                ? `Scale config "${config.metadata.name}" is active. Availability managed by schedule.` 
                : "No individual scaling config configured for this namespace."}
            </p>
          </div>
        </div>
        <div className="relative z-10 flex items-center gap-3">
          {config ? (
            <>
              <button 
                onClick={() => handleManualScale(true)}
                disabled={isScaling}
                className={`p-3 rounded-xl transition-all ${config.status?.phase === 'ScaledUp' ? 'bg-emerald-500 text-white shadow-lg shadow-emerald-500/30' : 'bg-slate-800 text-slate-400 hover:text-white'} ${isScaling ? 'opacity-50 cursor-not-allowed' : ''}`}
              >
                {isScaling && config.status?.phase === 'Scaling...' ? (
                   <div className="w-5 h-5 border-2 border-slate-300 border-t-emerald-500 rounded-full animate-spin"></div>
                ) : (
                  <Play size={20} fill={config.status?.phase === 'ScaledUp' ? "currentColor" : "none"} />
                )}
              </button>
              <button 
                onClick={() => handleManualScale(false)}
                disabled={isScaling}
                className={`p-3 rounded-xl transition-all ${config.status?.phase === 'ScaledDown' ? 'bg-rose-500 text-white shadow-lg shadow-rose-500/30' : 'bg-slate-800 text-slate-400 hover:text-white'} ${isScaling ? 'opacity-50 cursor-not-allowed' : ''}`}
              >
                {isScaling && config.status?.phase === 'Scaling...' ? (
                   <div className="w-5 h-5 border-2 border-slate-300 border-t-rose-500 rounded-full animate-spin"></div>
                ) : (
                  <Square size={20} fill={config.status?.phase === 'ScaledDown' ? "currentColor" : "none"} />
                )}
              </button>
              <button 
                onClick={() => setIsEditingConfig(true)}
                className="bg-white/10 hover:bg-white/20 p-3 rounded-xl transition-all"
              >
                <Settings2 size={20} />
              </button>
            </>
          ) : (
            <button 
              onClick={handleCreateConfig}
              className="bg-indigo-600 hover:bg-indigo-700 text-white px-6 py-3 rounded-xl font-bold flex items-center gap-2 shadow-lg shadow-indigo-600/20 transition-all"
            >
              <Plus size={18} /> Enable Scaling
            </button>
          )}
        </div>
      </div>

      {/* Controls */}
      <div className="bg-white p-4 rounded-xl shadow-sm border border-slate-200 mb-6 flex justify-between items-center">
        <div className="relative w-96">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" size={18} />
          <input
            type="text"
            placeholder="Search pods..."
            value={filterQuery}
            onChange={(e) => setFilterQuery(e.target.value)}
            className="pl-10 pr-4 py-2 bg-slate-50 border border-slate-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-emerald-500 focus:border-emerald-500 w-full transition-shadow"
          />
        </div>
        <div className="flex gap-4">
          <div className="px-3 py-1 bg-blue-50 text-blue-700 rounded-lg text-sm font-medium border border-blue-100 flex items-center gap-2">
            <Activity size={14} />
            {pods.length} Total Pods
          </div>
        </div>
      </div>

      {error && (
        <div className="bg-red-50 text-red-700 p-4 rounded-lg mb-6 flex items-center gap-2">
          <AlertCircle size={18} />
          {error}
        </div>
      )}

      {/* Pods Table */}
      <div className="bg-white rounded-xl shadow-sm border border-slate-200 overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-left border-collapse">
            <thead>
              <tr className="bg-slate-50 border-b border-slate-200">
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-500 uppercase tracking-wider cursor-pointer hover:bg-slate-100 transition-colors"
                  onClick={() => handleSort('name')}
                >
                  Pod Name {sortField === 'name' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-500 uppercase tracking-wider cursor-pointer hover:bg-slate-100 transition-colors"
                  onClick={() => handleSort('status')}
                >
                  Status {sortField === 'status' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-blue-50/30 cursor-pointer hover:bg-blue-100/30 transition-colors"
                  onClick={() => handleSort('cpuUsage')}
                >
                  CPU Usage {sortField === 'cpuUsage' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-blue-50/30 cursor-pointer hover:bg-blue-100/30 transition-colors"
                  onClick={() => handleSort('cpuReq')}
                >
                  CPU Req {sortField === 'cpuReq' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-blue-50/30 border-r border-slate-100 cursor-pointer hover:bg-blue-100/30 transition-colors"
                  onClick={() => handleSort('cpuLim')}
                >
                  CPU Lim {sortField === 'cpuLim' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-indigo-50/30 cursor-pointer hover:bg-indigo-100/30 transition-colors"
                  onClick={() => handleSort('memUsage')}
                >
                  RAM Usage {sortField === 'memUsage' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-indigo-50/30 cursor-pointer hover:bg-indigo-100/30 transition-colors"
                  onClick={() => handleSort('memReq')}
                >
                  RAM Req {sortField === 'memReq' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-indigo-50/30 cursor-pointer hover:bg-indigo-100/30 transition-colors"
                  onClick={() => handleSort('memLim')}
                >
                  RAM Lim {sortField === 'memLim' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {loading ? (
                <tr>
                  <td colSpan={8} className="px-6 py-12 text-center text-slate-400">
                    <div className="flex justify-center items-center gap-2">
                      <div className="animate-spin rounded-full h-4 w-4 border-b-2 border-emerald-500"></div>
                      Loading pod data...
                    </div>
                  </td>
                </tr>
              ) : filteredPods.length > 0 ? (
                filteredPods.map(pod => {
                  const opt = getOptimizationForPod(pod.name);
                  return (
                    <tr key={pod.name} className="hover:bg-slate-50/50 transition-colors">
                      <td className="px-6 py-4">
                        <div className="font-medium text-slate-800">{pod.name}</div>
                      </td>
                      <td className="px-6 py-4">
                        <span className={`px-2 py-0.5 rounded-full text-[10px] font-bold uppercase ${
                          pod.status === 'Running' ? 'bg-emerald-100 text-emerald-700' : 'bg-amber-100 text-amber-700'
                        }`}>
                          {pod.status}
                        </span>
                      </td>
                      {/* CPU Group */}
                      <td className="px-6 py-4 bg-blue-50/10 font-mono text-xs">{formatCpu(pod.cpu.usage)}</td>
                      <td className="px-6 py-4 bg-blue-50/10 font-mono text-xs text-slate-500">
                        {opt ? (
                          <div className="flex flex-col animate-in fade-in slide-in-from-left duration-500">
                            <span className="line-through opacity-40 text-[10px]">{formatCpu(opt.original.cpuRequest)}</span>
                            <span className="text-emerald-600 font-black flex items-center gap-1">
                              {formatCpu(pod.cpu.requests)}
                              <div className="w-1.5 h-1.5 bg-emerald-500 rounded-full animate-pulse shadow-[0_0_5px_rgba(16,185,129,0.5)]" />
                            </span>
                          </div>
                        ) : formatCpu(pod.cpu.requests)}
                      </td>
                      <td className="px-6 py-4 bg-blue-50/10 font-mono text-xs text-slate-500 border-r border-slate-50">
                        {opt ? (
                          <div className="flex flex-col">
                            <span className="line-through opacity-50">{formatCpu(opt.original.cpuLimit)}</span>
                            <span className="text-emerald-600 font-bold">{formatCpu(pod.cpu.limits)}</span>
                          </div>
                        ) : formatCpu(pod.cpu.limits)}
                      </td>
                      {/* RAM Group */}
                      <td className="px-6 py-4 bg-indigo-50/10 font-mono text-xs">{formatMem(pod.memory.usage)}</td>
                      <td className="px-6 py-4 bg-indigo-50/10 font-mono text-xs text-slate-500">
                        {opt ? (
                          <div className="flex flex-col">
                            <span className="line-through opacity-50">{formatMem(opt.original.memoryRequest)}</span>
                            <span className="text-emerald-600 font-bold">{formatMem(pod.memory.requests)}</span>
                          </div>
                        ) : formatMem(pod.memory.requests)}
                      </td>
                      <td className="px-6 py-4 bg-indigo-50/10 font-mono text-xs text-slate-500">
                        {opt ? (
                          <div className="flex flex-col">
                            <span className="line-through opacity-50">{formatMem(opt.original.memoryLimit)}</span>
                            <span className="text-emerald-600 font-bold">{formatMem(pod.memory.limits)}</span>
                          </div>
                        ) : formatMem(pod.memory.limits)}
                      </td>
                    </tr>
                  )
                })
              ) : (
                <tr>
                  <td colSpan={8} className="px-6 py-12 text-center text-slate-400">
                    No pods found in this namespace.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {isEditingConfig && config && (
        <ScalingConfigModal 
          name={config.metadata.name}
          mode="schedule"
          spec={config.spec}
          onClose={() => setIsEditingConfig(false)}
          onSave={handleUpdateConfig}
        />
      )}
    </div>
  )
}

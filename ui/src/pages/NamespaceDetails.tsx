import { useState } from 'react'
import { ArrowLeft, Search, Activity, AlertCircle } from 'lucide-react'
import NamespaceScalingPanel from '../components/NamespaceScalingPanel'
import InfoTooltip from '../components/InfoTooltip'
import RecommendationsPanel from '../components/RecommendationsPanel'
import { fetchNamespaceCost } from '../lib/api'
import type { CostEstimate, OptimizationStatus } from '../lib/types'
import { usePolling } from '../lib/usePolling'

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
  cost?: {
    hourlyCost: number;
    monthlyCost: number;
    currency: string;
  };
}

type SortField = 'name' | 'status' | 'cost' | 'cpuUsage' | 'cpuReq' | 'cpuLim' | 'memUsage' | 'memReq' | 'memLim'

interface NamespaceDetailsProps {
  namespace: string;
  onBack: () => void;
}

const formatCpu = (v?: string): string => {
  if (!v || v === '0') return '0';
  if (v.endsWith('n')) return (parseInt(v.slice(0, -1), 10) / 1000000000).toFixed(3);
  if (v.endsWith('u')) return (parseInt(v.slice(0, -1), 10) / 1000000).toFixed(3);
  if (v.endsWith('m')) return (parseInt(v.slice(0, -1), 10) / 1000).toFixed(3);
  return parseFloat(v).toFixed(3);
}

const formatMem = (v?: string): string => {
  if (!v || v === '0') return '0';
  let bytes = 0;
  const val = v.toLowerCase();
  if (val.endsWith('ki')) bytes = parseInt(v) * 1024;
  else if (val.endsWith('mi')) bytes = parseInt(v) * 1024 * 1024;
  else if (val.endsWith('gi')) bytes = parseInt(v) * 1024 * 1024 * 1024;
  else bytes = parseInt(v);
  
  return (bytes / (1024 * 1024)).toFixed(1) + ' MiB';
}

const sortValue = (pod: PodDetail, field: SortField): string | number => {
  switch (field) {
    case 'name': return pod.name
    case 'status': return pod.status
    case 'cost': return pod.cost?.monthlyCost ?? 0
    case 'cpuUsage': return parseFloat(formatCpu(pod.cpu.usage))
    case 'cpuReq': return parseFloat(formatCpu(pod.cpu.requests))
    case 'cpuLim': return parseFloat(formatCpu(pod.cpu.limits))
    case 'memUsage': return parseFloat(formatMem(pod.memory.usage))
    case 'memReq': return parseFloat(formatMem(pod.memory.requests))
    case 'memLim': return parseFloat(formatMem(pod.memory.limits))
  }
}

export default function NamespaceDetails({ namespace, onBack }: NamespaceDetailsProps) {
  const [pods, setPods] = useState<PodDetail[]>([])
  const [optimization, setOptimization] = useState<OptimizationStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [filterQuery, setFilterQuery] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [sortField, setSortField] = useState<SortField>('name')
  const [sortDirection, setSortDirection] = useState<'asc' | 'desc'>('asc')
  const [namespaceCost, setNamespaceCost] = useState<CostEstimate | null>(null)

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
      .finally(() => setLoading(false));
  }



  const fetchOptimization = () => {
    fetch(`/api/namespaces/${namespace}/optimization`)
      .then(res => res.json())
      .then(data => setOptimization(data))
      .catch(console.error);
  }

  const fetchCost = () => {
    fetchNamespaceCost(namespace)
      .then(cost => { if (cost) setNamespaceCost(cost) })
      .catch(e => console.error('Failed to fetch cost', e))
  }

  // Pods refresh silently: the loading state starts true and the first fetch clears it.
  usePolling(() => {
    fetchPods(true)
    fetchOptimization()
    fetchCost()
  }, 10000, namespace)







  const handleSort = (field: typeof sortField) => {
    if (sortField === field) {
      setSortDirection(sortDirection === 'asc' ? 'desc' : 'asc')
    } else {
      setSortField(field)
      setSortDirection('asc')
    }
  }

  const getSortedPods = () => {
    const dir = sortDirection === 'asc' ? 1 : -1;
    return [...pods].sort((a, b) => {
      const valA = sortValue(a, sortField);
      const valB = sortValue(b, sortField);
      if (valA < valB) return -dir;
      if (valA > valB) return dir;
      return 0;
    })
  }

  const filteredPods = getSortedPods().filter(pod => 
    pod.name.toLowerCase().includes(filterQuery.toLowerCase())
  )

  const getOptimizationForPod = (podName: string) => {
    if (!optimization?.active || !optimization.workloads) return null;
    return optimization.workloads.find(w => podName.startsWith(w.name));
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
            <h2 className="text-2xl font-bold tracking-tight text-slate-900 flex items-center gap-2">{namespace}</h2>
            <p className="mt-1 text-sm text-slate-500">Usage, cost, scaling and right-sizing for this namespace</p>
          </div>
          <InfoTooltip content="This view shows real-time metrics for each pod. Strike-through values indicate optimized resources. Green values are currently active." position="bottom" />
        </div>
      </div>

      <NamespaceScalingPanel namespace={namespace} />

      <RecommendationsPanel namespace={namespace} />

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
          <div className="px-3 py-1 bg-brand-50 text-brand-700 rounded-lg text-sm font-medium border border-brand-100 flex items-center gap-2">
            <Activity size={14} />
            {pods.length} Total Pods
          </div>
          {namespaceCost && (
            <div className="flex flex-col items-end">
              <span className="text-xl font-bold text-slate-900 flex items-center gap-1.5">
                ${namespaceCost.hourlyCost.toFixed(4)}/hr • ${namespaceCost.monthlyCost.toFixed(2)}/mo
              </span>
            </div>
          )}
        </div>
      </div>

      {error && (
        <div className="bg-rose-50 text-rose-700 p-4 rounded-lg mb-6 flex items-center gap-2">
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
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-brand-50/30 cursor-pointer hover:bg-brand-100/30 transition-colors"
                  onClick={() => handleSort('cpuUsage')}
                >
                  CPU Usage {sortField === 'cpuUsage' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-brand-50/30 cursor-pointer hover:bg-brand-100/30 transition-colors"
                  onClick={() => handleSort('cpuReq')}
                >
                  CPU Req {sortField === 'cpuReq' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-brand-50/30 border-r border-slate-100 cursor-pointer hover:bg-brand-100/30 transition-colors"
                  onClick={() => handleSort('cpuLim')}
                >
                  CPU Lim {sortField === 'cpuLim' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-brand-50/30 cursor-pointer hover:bg-brand-100/30 transition-colors"
                  onClick={() => handleSort('memUsage')}
                >
                  RAM Usage {sortField === 'memUsage' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-brand-50/30 cursor-pointer hover:bg-brand-100/30 transition-colors"
                  onClick={() => handleSort('memReq')}
                >
                  RAM Req {sortField === 'memReq' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-slate-600 uppercase tracking-wider bg-brand-50/30 cursor-pointer hover:bg-brand-100/30 transition-colors border-r border-slate-100"
                  onClick={() => handleSort('memLim')}
                >
                  RAM Lim {sortField === 'memLim' && (sortDirection === 'asc' ? '↑' : '↓')}
                </th>
                <th 
                  className="px-6 py-4 text-xs font-semibold text-emerald-600 uppercase tracking-wider bg-emerald-50/30 cursor-pointer hover:bg-emerald-100/30 transition-colors"
                  onClick={() => handleSort('cost')}
                >
                  Est. Cost {sortField === 'cost' && (sortDirection === 'asc' ? '↑' : '↓')}
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
                      <td className="px-6 py-4 bg-brand-50/10 font-mono text-xs">{formatCpu(pod.cpu.usage)}</td>
                      <td className="px-6 py-4 bg-brand-50/10 font-mono text-xs text-slate-500">
                        {opt ? (
                          <div className="flex flex-col animate-in fade-in slide-in-from-left duration-500">
                            <span className="line-through opacity-40 text-[10px]">{formatCpu(opt.original.cpuRequest)}</span>
                            <span className="text-emerald-600 font-bold flex items-center gap-1">
                              {formatCpu(pod.cpu.requests)}
                              <div className="w-1.5 h-1.5 bg-emerald-500 rounded-full animate-pulse shadow-[0_0_5px_rgba(16,185,129,0.5)]" />
                            </span>
                          </div>
                        ) : formatCpu(pod.cpu.requests)}
                      </td>
                      <td className="px-6 py-4 bg-brand-50/10 font-mono text-xs text-slate-500 border-r border-slate-50">
                        {opt ? (
                          <div className="flex flex-col">
                            <span className="line-through opacity-50">{formatCpu(opt.original.cpuLimit)}</span>
                            <span className="text-emerald-600 font-bold">{formatCpu(pod.cpu.limits)}</span>
                          </div>
                        ) : formatCpu(pod.cpu.limits)}
                      </td>
                      {/* RAM Group */}
                      <td className="px-6 py-4 bg-brand-50/10 font-mono text-xs">{formatMem(pod.memory.usage)}</td>
                      <td className="px-6 py-4 bg-brand-50/10 font-mono text-xs text-slate-500">
                        {opt ? (
                          <div className="flex flex-col">
                            <span className="line-through opacity-50">{formatMem(opt.original.memoryRequest)}</span>
                            <span className="text-emerald-600 font-bold">{formatMem(pod.memory.requests)}</span>
                          </div>
                        ) : formatMem(pod.memory.requests)}
                      </td>
                      <td className="px-6 py-4 bg-brand-50/10 font-mono text-xs text-slate-500 border-r border-slate-50">
                        {opt ? (
                          <div className="flex flex-col">
                            <span className="line-through opacity-50">{formatMem(opt.original.memoryLimit)}</span>
                            <span className="text-emerald-600 font-bold">{formatMem(pod.memory.limits)}</span>
                          </div>
                        ) : formatMem(pod.memory.limits)}
                      </td>
                      <td className="px-6 py-4 bg-emerald-50/10">
                        {pod.cost ? (
                          <div className="flex flex-col animate-in fade-in zoom-in duration-300">
                            <span className="text-emerald-600 font-bold text-xs">${pod.cost.monthlyCost.toFixed(2)}<span className="text-[10px] font-medium opacity-70">/mo</span></span>
                            <span className="text-emerald-400 font-medium text-[10px]">${pod.cost.hourlyCost.toFixed(4)}/hr</span>
                          </div>
                        ) : (
                          <span className="text-slate-400 text-xs">-</span>
                        )}
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
    </div>
  )
}

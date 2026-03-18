import { useState, useEffect } from 'react'
import { ArrowLeft, Play, Square, RefreshCw } from 'lucide-react'

interface Workload {
  name: string
  kind: string
  replicas: number
  readyReplicas: number
  status: string
}

interface ScalingWorkloadsProps {
  namespace: string
  onBack: () => void
}

export default function ScalingWorkloads({ namespace, onBack }: ScalingWorkloadsProps) {
  const [workloads, setWorkloads] = useState<Workload[]>([])
  const [loading, setLoading] = useState(true)

  const fetchWorkloads = async () => {
    try {
      const res = await fetch(`/api/namespaces/${namespace}/workloads`)
      const data = await res.json()
      setWorkloads(data || [])
    } catch { setWorkloads([]) }
    finally { setLoading(false) }
  }

  useEffect(() => { fetchWorkloads() }, [namespace])

  // Scale individual workload via the scaling endpoint
  const handleScaleWorkload = async (name: string, kind: string, replicas: number) => {
    try {
      await fetch(`/api/namespaces/${namespace}/workloads/${name}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind, replicas })
      })
      // Refresh after a short delay for kubernetes to process
      setTimeout(fetchWorkloads, 1000)
    } catch (err) {
      console.error("Failed to scale workload", err)
    }
  }

  const runningCount = workloads.filter(w => w.status === 'running').length
  const scaledDownCount = workloads.filter(w => w.status === 'scaled-down').length

  return (
    <div className="p-8 max-w-7xl mx-auto min-h-full">
      {/* Header */}
      <div className="flex items-center gap-4 mb-8">
        <button onClick={onBack} className="p-2 rounded-xl bg-white border border-slate-200 hover:bg-slate-50 transition-colors shadow-sm">
          <ArrowLeft size={20} className="text-slate-600" />
        </button>
        <div className="flex-1">
          <h1 className="text-3xl font-black text-slate-800 tracking-tight">{namespace}</h1>
          <p className="text-sm text-slate-500 font-medium">Workload scaling for namespace</p>
        </div>
        <button onClick={fetchWorkloads} className="flex items-center gap-2 px-4 py-2 bg-white border border-slate-200 rounded-xl text-sm font-bold text-slate-600 hover:bg-slate-50 transition-colors shadow-sm">
          <RefreshCw size={16} />
          Refresh
        </button>
      </div>

      {/* Summary Cards */}
      <div className="grid grid-cols-3 gap-4 mb-8">
        <div className="bg-white rounded-2xl border border-slate-200 p-5 shadow-sm">
          <div className="text-3xl font-black text-slate-800">{workloads.length}</div>
          <div className="text-xs font-bold text-slate-400 uppercase tracking-wider mt-1">Total Workloads</div>
        </div>
        <div className="bg-emerald-50 rounded-2xl border border-emerald-100 p-5">
          <div className="text-3xl font-black text-emerald-600">{runningCount}</div>
          <div className="text-xs font-bold text-emerald-500 uppercase tracking-wider mt-1">Running</div>
        </div>
        <div className="bg-slate-50 rounded-2xl border border-slate-200 p-5">
          <div className="text-3xl font-black text-slate-500">{scaledDownCount}</div>
          <div className="text-xs font-bold text-slate-400 uppercase tracking-wider mt-1">Scaled Down</div>
        </div>
      </div>

      {/* Bulk Actions */}
      <div className="flex items-center gap-3 mb-6">
        <button 
          onClick={() => {
            workloads.forEach(w => {
              if (w.status === 'scaled-down') handleScaleWorkload(w.name, w.kind, 1)
            })
          }}
          className="flex items-center gap-2 px-5 py-3 bg-emerald-500 text-white rounded-xl font-bold text-sm shadow-lg shadow-emerald-500/20 hover:bg-emerald-600 transition-all"
        >
          <Play size={16} fill="currentColor" /> Scale Up All
        </button>
        <button 
          onClick={() => {
            workloads.forEach(w => {
              if (w.status === 'running') handleScaleWorkload(w.name, w.kind, 0)
            })
          }}
          className="flex items-center gap-2 px-5 py-3 bg-rose-500 text-white rounded-xl font-bold text-sm shadow-lg shadow-rose-500/20 hover:bg-rose-600 transition-all"
        >
          <Square size={16} fill="currentColor" /> Scale Down All
        </button>
      </div>

      {/* Workloads Table */}
      {loading ? (
        <div className="text-center py-20 text-slate-400">Loading workloads...</div>
      ) : workloads.length === 0 ? (
        <div className="text-center py-20 text-slate-400">No workloads found in this namespace</div>
      ) : (
        <div className="bg-white rounded-2xl border border-slate-200 shadow-sm overflow-hidden">
          <table className="w-full">
            <thead>
              <tr className="bg-slate-50 border-b border-slate-100">
                <th className="text-left px-6 py-4 text-xs font-bold text-slate-500 uppercase tracking-wider">Status</th>
                <th className="text-left px-6 py-4 text-xs font-bold text-slate-500 uppercase tracking-wider">Name</th>
                <th className="text-left px-6 py-4 text-xs font-bold text-slate-500 uppercase tracking-wider">Kind</th>
                <th className="text-left px-6 py-4 text-xs font-bold text-slate-500 uppercase tracking-wider">Replicas</th>
                <th className="text-right px-6 py-4 text-xs font-bold text-slate-500 uppercase tracking-wider">Actions</th>
              </tr>
            </thead>
            <tbody>
              {workloads.map(w => (
                <tr key={`${w.kind}-${w.name}`} className="border-b border-slate-50 hover:bg-slate-50/50 transition-colors">
                  <td className="px-6 py-4">
                    <div className={`w-3 h-3 rounded-full ${w.status === 'running' ? 'bg-emerald-500' : 'bg-slate-300'}`} />
                  </td>
                  <td className="px-6 py-4">
                    <span className="font-bold text-slate-800 text-sm">{w.name}</span>
                  </td>
                  <td className="px-6 py-4">
                    <span className="text-[10px] bg-slate-100 text-slate-500 px-2.5 py-1 rounded-lg font-bold uppercase">{w.kind}</span>
                  </td>
                  <td className="px-6 py-4">
                    <span className={`font-bold text-sm ${w.status === 'running' ? 'text-emerald-600' : 'text-slate-400'}`}>
                      {w.readyReplicas}/{w.replicas}
                    </span>
                    <span className="text-xs text-slate-400 ml-1">ready</span>
                  </td>
                  <td className="px-6 py-4 text-right">
                    <div className="flex items-center justify-end gap-2">
                      <button
                        onClick={() => handleScaleWorkload(w.name, w.kind, w.replicas > 0 ? w.replicas : 1)}
                        disabled={w.status === 'running'}
                        className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-bold transition-all ${
                          w.status === 'running' 
                            ? 'bg-emerald-50 text-emerald-400 cursor-default' 
                            : 'bg-emerald-500 text-white hover:bg-emerald-600 shadow-sm'
                        }`}
                      >
                        <Play size={12} fill="currentColor" />
                        {w.status === 'running' ? 'Running' : 'Scale Up'}
                      </button>
                      <button
                        onClick={() => handleScaleWorkload(w.name, w.kind, 0)}
                        disabled={w.status === 'scaled-down'}
                        className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-bold transition-all ${
                          w.status === 'scaled-down' 
                            ? 'bg-slate-50 text-slate-300 cursor-default'
                            : 'bg-rose-500 text-white hover:bg-rose-600 shadow-sm'
                        }`}
                      >
                        <Square size={12} fill="currentColor" />
                        {w.status === 'scaled-down' ? 'Stopped' : 'Scale Down'}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

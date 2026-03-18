import { useState, useEffect } from 'react'
import { Server, Cpu, Database, Activity, Globe } from 'lucide-react'
import InfoTooltip from '../components/InfoTooltip'

interface NodeData {
  name: string;
  status: string;
  cpu: {
    used: number;
    requested: number;
    capacity: number;
  };
  mem: {
    used: number;
    requested: number;
    capacity: number;
  };
  info: {
    os: string;
    arch: string;
    kernel: string;
    kubelet: string;
  };
}

interface ClusterResponse {
  k8sVersion: string;
  totalCapacity: {
    cpu: number;
    mem: number;
  };
  totalUsage: {
    cpu: number;
    mem: number;
  };
  totalRequested: {
    cpu: number;
    mem: number;
  };
  nodes: NodeData[];
}

export default function ClusterDashboard() {
  const [data, setData] = useState<ClusterResponse | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch('/api/cluster/nodes')
      .then(res => res.json())
      .then(d => {
        setData(d)
        setLoading(false)
      })
      .catch(err => {
        console.error(err)
        setLoading(false)
      })
  }, [])

  if (loading) {
    return (
      <div className="p-8 flex items-center justify-center h-full">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-emerald-500"></div>
      </div>
    )
  }

  if (!data) return <div className="p-8">No data available</div>

  const getUsageColor = (percent: number) => {
    if (percent > 90) return 'bg-red-500'
    if (percent > 70) return 'bg-orange-500'
    if (percent > 50) return 'bg-amber-400'
    return 'bg-emerald-500'
  }

  const getUsageText = (percent: number) => {
    if (percent > 90) return 'text-red-600'
    if (percent > 70) return 'text-orange-600'
    return 'text-emerald-600'
  }

  return (
    <div className="p-8 space-y-8 animate-in fade-in duration-500">
      {/* Header Info */}
      <div className="flex justify-between items-end">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-3xl font-black text-slate-900 tracking-tight flex items-center gap-3 uppercase">
              <Activity className="text-emerald-500" size={32} />
              Cluster Node Map
            </h1>
            <InfoTooltip content="This view shows all nodes in your cluster. Usage bars use a heatmap color scheme: Green (<50%), Amber (50-70%), Orange (70-90%), and Red (>90%). It displays both 'Requested' (allocation) and 'Actual Usage' (live metrics)." position="bottom" />
          </div>
          <p className="text-slate-500 mt-1 font-medium italic">Real-time infrastructure capacity and heatmap utilization</p>
        </div>
        <div className="bg-slate-900 text-white px-4 py-2 rounded-xl shadow-lg border border-slate-800 flex items-center gap-3">
          <Globe size={18} className="text-emerald-400" />
          <div className="flex flex-col">
            <span className="text-[10px] uppercase tracking-wider text-slate-500 font-bold">K8s Version</span>
            <span className="font-mono text-sm font-bold">{data.k8sVersion}</span>
          </div>
        </div>
      </div>

      {/* Cluster Overview Stats */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div className="bg-white p-6 rounded-3xl shadow-sm border border-slate-100 flex items-center gap-5">
          <div className="w-14 h-14 bg-emerald-50 rounded-2xl flex items-center justify-center text-emerald-600">
            <Server size={28} />
          </div>
          <div>
            <p className="text-sm font-bold text-slate-400 uppercase tracking-wider">Nodes</p>
            <p className="text-3xl font-black text-slate-900">{data.nodes.length}</p>
          </div>
        </div>

        <div className="bg-white p-6 rounded-3xl shadow-sm border border-slate-100">
          <div className="flex justify-between items-start mb-4">
            <div className="flex items-center gap-3">
              <div className="p-2 bg-blue-50 text-blue-600 rounded-lg">
                <Cpu size={20} />
              </div>
              <span className="text-sm font-bold text-slate-400 uppercase tracking-wider">Total CPU</span>
            </div>
            <div className="text-right">
              <div className={`text-lg font-black ${getUsageText((data.totalRequested?.cpu / data.totalCapacity.cpu) * 100 || 0)}`}>
                {((data.totalRequested?.cpu / data.totalCapacity.cpu) * 100 || 0).toFixed(1)}% Req
              </div>
              <div className={`text-xs font-bold ${getUsageText((data.totalUsage.cpu / data.totalCapacity.cpu) * 100)}`}>
                {((data.totalUsage.cpu / data.totalCapacity.cpu) * 100).toFixed(1)}% Act
              </div>
            </div>
          </div>
          <div className="w-full bg-slate-100 h-3 rounded-full overflow-hidden relative mb-2">
            <div 
              className={`h-full opacity-30 absolute top-0 left-0 transition-all duration-1000 ${getUsageColor((data.totalRequested?.cpu / data.totalCapacity.cpu) * 100 || 0)}`}
              style={{ width: `${(data.totalRequested?.cpu / data.totalCapacity.cpu) * 100 || 0}%` }}
            />
            <div 
              className={`h-full absolute top-0 left-0 transition-all duration-1000 ${getUsageColor((data.totalUsage.cpu / data.totalCapacity.cpu) * 100)}`}
              style={{ width: `${(data.totalUsage.cpu / data.totalCapacity.cpu) * 100}%` }}
            />
          </div>
          <div className="flex justify-between text-[10px] font-bold text-slate-400 uppercase">
            <span>{data.totalRequested?.cpu?.toFixed(2) || '0'} Req / {data.totalUsage.cpu.toFixed(2)} Act</span>
            <span>{data.totalCapacity.cpu.toFixed(0)} Cores Total</span>
          </div>
        </div>

        <div className="bg-white p-6 rounded-3xl shadow-sm border border-slate-100">
          <div className="flex justify-between items-start mb-4">
            <div className="flex items-center gap-3">
              <div className="p-2 bg-purple-50 text-purple-600 rounded-lg">
                <Database size={20} />
              </div>
              <span className="text-sm font-bold text-slate-400 uppercase tracking-wider">Total RAM</span>
            </div>
            <div className="text-right">
              <div className={`text-lg font-black ${getUsageText((data.totalRequested?.mem / data.totalCapacity.mem) * 100 || 0)}`}>
                {((data.totalRequested?.mem / data.totalCapacity.mem) * 100 || 0).toFixed(1)}% Req
              </div>
              <div className={`text-xs font-bold ${getUsageText((data.totalUsage.mem / data.totalCapacity.mem) * 100)}`}>
                {((data.totalUsage.mem / data.totalCapacity.mem) * 100).toFixed(1)}% Act
              </div>
            </div>
          </div>
          <div className="w-full bg-slate-100 h-3 rounded-full overflow-hidden relative mb-2">
            <div 
              className={`h-full opacity-30 absolute top-0 left-0 transition-all duration-1000 ${getUsageColor((data.totalRequested?.mem / data.totalCapacity.mem) * 100 || 0)}`}
              style={{ width: `${(data.totalRequested?.mem / data.totalCapacity.mem) * 100 || 0}%` }}
            />
            <div 
              className={`h-full absolute top-0 left-0 transition-all duration-1000 ${getUsageColor((data.totalUsage.mem / data.totalCapacity.mem) * 100)}`}
              style={{ width: `${(data.totalUsage.mem / data.totalCapacity.mem) * 100}%` }}
            />
          </div>
          <div className="flex justify-between text-[10px] font-bold text-slate-400 uppercase">
            <span>{((data.totalRequested?.mem || 0) / 1024 / 1024 / 1024).toFixed(1)} Req / {(data.totalUsage.mem / 1024 / 1024 / 1024).toFixed(1)} Act</span>
            <span>{(data.totalCapacity.mem / 1024 / 1024 / 1024).toFixed(1)} GiB Total</span>
          </div>
        </div>
      </div>

      {/* Nodes Table/Grid */}
      <div className="bg-white rounded-3xl shadow-sm border border-slate-100 overflow-hidden">
        <div className="px-8 py-6 border-b border-slate-50 flex items-center justify-between bg-slate-50/50">
          <h2 className="font-bold text-slate-800 tracking-tight">Node breakdown</h2>
          <span className="text-[10px] font-black bg-slate-200 text-slate-600 px-2 py-1 rounded-md uppercase tracking-tighter">
            {data.nodes.length} Active
          </span>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-left">
            <thead>
              <tr className="text-[10px] font-black text-slate-400 uppercase tracking-widest bg-slate-50/30">
                <th className="px-8 py-4">Node Name</th>
                <th className="px-4 py-4 text-center">Status</th>
                <th className="px-4 py-4">CPU Allocation & Usage</th>
                <th className="px-4 py-4">RAM Allocation & Usage</th>
                <th className="px-8 py-4 text-right">Environment Info</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-50">
              {data.nodes.map(node => {
                const cpuUsePct = (node.cpu.used / node.cpu.capacity) * 100;
                const memUsePct = (node.mem.used / node.mem.capacity) * 100;
                const cpuReqPct = ((node.cpu.requested || 0) / node.cpu.capacity) * 100;
                const memReqPct = ((node.mem.requested || 0) / node.mem.capacity) * 100;

                return (
                  <tr key={node.name} className="hover:bg-slate-50/80 transition-colors group">
                    <td className="px-8 py-5">
                      <div className="flex items-center gap-3">
                        <div className="p-2 bg-slate-100 rounded-lg group-hover:bg-white border border-transparent group-hover:border-slate-100 transition-all">
                          <Server size={16} className="text-slate-500" />
                        </div>
                        <span className="font-bold text-slate-700">{node.name}</span>
                      </div>
                    </td>
                    <td className="px-4 py-5 text-center">
                      <span className={`px-2 py-1 rounded-md text-[10px] font-black uppercase tracking-tight ${
                        node.status === 'Ready' ? 'bg-emerald-50 text-emerald-600' : 'bg-red-50 text-red-600'
                      }`}>
                        {node.status}
                      </span>
                    </td>
                    <td className="px-4 py-5 w-[28%]">
                      <div className="flex flex-col gap-3 pr-4">
                        <div className="flex flex-col gap-1">
                          <div className="flex justify-between text-[9px] font-black uppercase tracking-wider">
                            <span className="text-slate-400">Requested</span>
                            <span className={getUsageText(cpuReqPct)}>{cpuReqPct.toFixed(1)}% <span className="text-slate-400">({(node.cpu.requested || 0).toFixed(2)} / {node.cpu.capacity} cores)</span></span>
                          </div>
                          <div className="w-full bg-slate-100 h-1.5 rounded-full overflow-hidden">
                            <div className={`h-full ${getUsageColor(cpuReqPct)} opacity-75`} style={{ width: `${Math.min(cpuReqPct, 100)}%` }} />
                          </div>
                        </div>
                        <div className="flex flex-col gap-1">
                          <div className="flex justify-between text-[9px] font-black uppercase tracking-wider">
                            <span className="text-slate-400">Actual Usage</span>
                            <span className={getUsageText(cpuUsePct)}>{cpuUsePct.toFixed(1)}% <span className="text-slate-400">({node.cpu.used.toFixed(2)} cores)</span></span>
                          </div>
                          <div className="w-full bg-slate-100 h-1.5 rounded-full overflow-hidden">
                            <div className={`h-full ${getUsageColor(cpuUsePct)}`} style={{ width: `${Math.min(cpuUsePct, 100)}%` }} />
                          </div>
                        </div>
                      </div>
                    </td>
                    <td className="px-4 py-5 w-[28%]">
                      <div className="flex flex-col gap-3 pr-4">
                        <div className="flex flex-col gap-1">
                          <div className="flex justify-between text-[9px] font-black uppercase tracking-wider">
                            <span className="text-slate-400">Requested</span>
                            <span className={getUsageText(memReqPct)}>{memReqPct.toFixed(1)}% <span className="text-slate-400">{((node.mem.requested || 0) / 1024 / 1024 / 1024).toFixed(1)} / {(node.mem.capacity / 1024 / 1024 / 1024).toFixed(0)} GiB</span></span>
                          </div>
                          <div className="w-full bg-slate-100 h-1.5 rounded-full overflow-hidden">
                            <div className={`h-full ${getUsageColor(memReqPct)} opacity-75`} style={{ width: `${Math.min(memReqPct, 100)}%` }} />
                          </div>
                        </div>
                        <div className="flex flex-col gap-1">
                          <div className="flex justify-between text-[9px] font-black uppercase tracking-wider">
                            <span className="text-slate-400">Actual Usage</span>
                            <span className={getUsageText(memUsePct)}>{memUsePct.toFixed(1)}% <span className="text-slate-400">{(node.mem.used / 1024 / 1024 / 1024).toFixed(1)} GiB</span></span>
                          </div>
                          <div className="w-full bg-slate-100 h-1.5 rounded-full overflow-hidden">
                            <div className={`h-full ${getUsageColor(memUsePct)}`} style={{ width: `${Math.min(memUsePct, 100)}%` }} />
                          </div>
                        </div>
                      </div>
                    </td>
                    <td className="px-8 py-5 text-right flex-1">
                      <div className="text-[10px] font-medium text-slate-500 flex flex-col items-end gap-0.5">
                        <span className="font-bold text-slate-400 uppercase tracking-tighter text-[9px]">OS: {node.info.os}</span>
                        <span>{node.info.kernel}</span>
                        <span className="bg-slate-100 px-1.5 py-0.5 rounded font-mono text-slate-600 mt-1">{node.info.kubelet}</span>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}


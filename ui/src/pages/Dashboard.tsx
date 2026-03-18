import { useState, useEffect } from 'react'
import { Filter, AlertCircle } from 'lucide-react'
import NamespaceCard from '../components/NamespaceCard'
import InfoTooltip from '../components/InfoTooltip'

// Define the namespace structure we will get from the API
export interface NamespaceFinOps {
  metadata: {
    name: string;
    creationTimestamp: string;
  };
  spec: {
    targetNamespace: string;
  };
  status?: {
    lastUpdated?: string;
    insights?: string[];
    history?: any[];
  };
}

interface DashboardProps {
  onSelectNamespace: (name: string) => void;
}

export default function Dashboard({ onSelectNamespace }: DashboardProps) {
  const [namespaces, setNamespaces] = useState<NamespaceFinOps[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filterQuery, setFilterQuery] = useState('')

  const fetchNamespaces = () => {
    fetch('/api/namespaces')
      .then(res => {
        if (!res.ok) throw new Error('API request failed')
        return res.json()
      })
      .then(data => {
        setNamespaces(data || [])
        setError(null)
      })
      .catch(err => {
        console.error("Failed to fetch namespaces", err)
        setError("Could not load namespaces. Is the Operator API running?")
      })
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    fetchNamespaces()
    const interval = setInterval(fetchNamespaces, 30000)
    return () => clearInterval(interval)
  }, [])

  const filteredNamespaces = namespaces.filter(ns => 
    ns.spec.targetNamespace.toLowerCase().includes(filterQuery.toLowerCase())
  ).reduce((acc: NamespaceFinOps[], current) => {
    const x = acc.find(item => item.spec.targetNamespace === current.spec.targetNamespace);
    if (!x) {
      return acc.concat([current]);
    } else {
      // If we find a duplicate, prefer the one that has status or more history
      if (current.status && !x.status) return acc.filter(i => i !== x).concat([current]);
      return acc;
    }
  }, []).sort((a, b) => a.spec.targetNamespace.localeCompare(b.spec.targetNamespace))

  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      <div className="flex justify-between items-center mb-8">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="text-3xl font-black tracking-tight text-slate-900 uppercase">Namespace Insights</h2>
            <InfoTooltip content="This dashboard identifies 'Overprovisioned' namespaces where allocated CPU/RAM significantly exceeds actual usage. Use the 'Optimize' button to reclaim resources." position="bottom" />
          </div>
          <p className="text-slate-500 mt-1 font-medium">Real-time resource utilization and overprovisioning analytics</p>
        </div>
        
        <div className="flex gap-4">
          <div className="relative">
            <Filter className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" size={18} />
            <input
              type="text"
              placeholder="Filter by namespace name..."
              value={filterQuery}
              onChange={(e) => setFilterQuery(e.target.value)}
              className="pl-10 pr-4 py-2 bg-white border border-slate-200 rounded-lg shadow-sm focus:outline-none focus:ring-2 focus:ring-emerald-500 focus:border-emerald-500 w-64 transition-shadow"
            />
          </div>
          {/* Add more filters (heavy/light/usage) here as needed */}
        </div>
      </div>

      {error && !import.meta.env.DEV && (
        <div className="bg-red-50 border-l-4 border-red-500 p-4 mb-8 rounded-r-lg">
          <div className="flex">
            <div className="flex-shrink-0">
              <AlertCircle className="h-5 w-5 text-red-500" />
            </div>
            <div className="ml-3">
              <p className="text-sm text-red-700">{error}</p>
            </div>
          </div>
        </div>
      )}

      {loading ? (
        <div className="flex justify-center items-center h-64">
          <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-emerald-500"></div>
        </div>
      ) : (
        <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
          {filteredNamespaces.length > 0 ? (
            filteredNamespaces.map(ns => (
              <NamespaceCard 
                key={ns.metadata.name} 
                namespace={ns.spec.targetNamespace} 
                insights={ns.status?.insights || []}
                onClick={() => onSelectNamespace(ns.spec.targetNamespace)}
              />
            ))
          ) : (
            <div className="col-span-full flex flex-col items-center justify-center p-12 bg-white rounded-xl border border-dashed border-slate-300">
              <p className="text-slate-500 text-lg">No namespaces found matching your filters.</p>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

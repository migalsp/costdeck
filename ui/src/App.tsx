import { useState, useEffect } from 'react'
import { Scaling, Server, LineChart, Activity, BookOpen, LogOut } from 'lucide-react'
import Dashboard from './pages/Dashboard'
import NamespaceDetails from './pages/NamespaceDetails'
import OperatorHealth from './pages/OperatorHealth'
import ScalingPage from './pages/ScalingPage'
import ScalingWorkloads from './pages/ScalingWorkloads'
import ClusterDashboard from './pages/ClusterDashboard'
import LoginPage from './pages/LoginPage'
import ApiReference from './pages/ApiReference'

function App() {
  const [activeTab, setActiveTab] = useState<'dashboard' | 'scale' | 'cluster' | 'operator' | 'api-docs'>('dashboard')
  const [selectedNamespace, setSelectedNamespace] = useState<string | null>(null)
  const [selectedScalingNS, setSelectedScalingNS] = useState<string | null>(null)
  const [appVersion, setAppVersion] = useState('...')
  const [isAuthenticated, setIsAuthenticated] = useState<boolean | null>(null)

  useEffect(() => {
    // Check auth status by calling any authenticated endpoint
    fetch('/api/version')
      .then(r => {
        if (r.status === 401) {
          setIsAuthenticated(false)
          return null
        }
        setIsAuthenticated(true)
        return r.json()
      })
      .then(d => { if (d) setAppVersion(d.version || 'dev') })
      .catch(() => setIsAuthenticated(true)) // If no auth configured, allow through
  }, [])

  // Loading state
  if (isAuthenticated === null) {
    return (
      <div className="min-h-screen bg-slate-900 flex items-center justify-center">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-emerald-500"></div>
      </div>
    )
  }

  // Login gate
  if (isAuthenticated === false) {
    return <LoginPage onLogin={() => {
      setIsAuthenticated(true)
      fetch('/api/version').then(r => r.json()).then(d => setAppVersion(d.version || 'dev')).catch(() => {})
    }} />
  }

  return (
    <div className="flex h-screen w-full bg-slate-50 font-sans">
      {/* Sidebar */}
      <aside className="w-66 bg-slate-900 text-white flex flex-col shadow-2xl z-10 border-r border-white/5 backdrop-blur-xl">
        <div className="p-7">
          <div className="flex items-center gap-4 mb-2">
            <div className="w-11 h-11 bg-emerald-500 rounded-2xl flex items-center justify-center shadow-lg shadow-emerald-500/30 ring-1 ring-white/20 animate-in zoom-in duration-500">
              <svg xmlns="http://www.w3.org/2000/svg" width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" className="text-white">
                <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" />
                <polyline points="7 14 10 11 13 14 17 9" />
                <line x1="17" y1="9" x2="17" y2="13" />
                <line x1="17" y1="9" x2="13" y2="9" />
              </svg>
            </div>
          <div className="flex flex-col">
            <h1 className="text-2xl font-black tracking-tighter text-white leading-none">Cost Deck</h1>
            <span className="text-[10px] uppercase tracking-[0.2em] text-emerald-500 font-extrabold mt-1">FinOps Platform</span>
          </div>
          </div>
        </div>

        <nav className="flex-1 px-4 mt-4 space-y-8">
          {/* Analytics Block */}
          <div>
            <div className="flex items-center gap-2 px-4 mb-4">
              <div className="h-px flex-1 bg-slate-800" />
              <h3 className="text-[10px] font-black uppercase tracking-widest text-slate-500 whitespace-nowrap">Analytics</h3>
              <div className="h-px flex-1 bg-slate-800" />
            </div>
            
            <div className="space-y-1.5">
              <button
                onClick={() => { setActiveTab('dashboard'); setSelectedNamespace(null); }}
                className={`w-full flex items-center gap-3 px-4 py-3 rounded-xl transition-all duration-300 relative group overflow-hidden ${
                  activeTab === 'dashboard'
                    ? 'bg-emerald-500 shadow-lg shadow-emerald-500/20 text-white'
                    : 'text-slate-400 hover:text-white hover:bg-slate-800/50'
                }`}
              >
                {activeTab === 'dashboard' && <div className="absolute left-0 top-0 bottom-0 w-1 bg-white rounded-r-full shadow-[0_0_10px_white]" />}
                <LineChart size={20} className={activeTab === 'dashboard' ? 'text-white' : 'text-slate-500 group-hover:text-emerald-400 transition-colors'} />
                <span className="font-bold text-[13px] tracking-tight">Namespace Insights</span>
              </button>

              <button
                onClick={() => setActiveTab('cluster')}
                className={`w-full flex items-center gap-3 px-4 py-3 rounded-xl transition-all duration-300 relative group overflow-hidden ${
                  activeTab === 'cluster'
                    ? 'bg-emerald-500 shadow-lg shadow-emerald-500/20 text-white'
                    : 'text-slate-400 hover:text-white hover:bg-slate-800/50'
                }`}
              >
                {activeTab === 'cluster' && <div className="absolute left-0 top-0 bottom-0 w-1 bg-white rounded-r-full shadow-[0_0_10px_white]" />}
                <Server size={20} className={activeTab === 'cluster' ? 'text-white' : 'text-slate-500 group-hover:text-emerald-400 transition-colors'} />
                <span className="font-bold text-[13px] tracking-tight">Cluster Node Map</span>
              </button>

              <button
                onClick={() => setActiveTab('operator')}
                className={`w-full flex items-center gap-3 px-4 py-3 rounded-xl transition-all duration-300 relative group overflow-hidden ${
                  activeTab === 'operator'
                    ? 'bg-emerald-500 shadow-lg shadow-emerald-500/20 text-white'
                    : 'text-slate-400 hover:text-white hover:bg-slate-800/50'
                }`}
              >
                {activeTab === 'operator' && <div className="absolute left-0 top-0 bottom-0 w-1 bg-white rounded-r-full shadow-[0_0_10px_white]" />}
                <Activity size={20} className={activeTab === 'operator' ? 'text-white' : 'text-slate-500 group-hover:text-emerald-400 transition-colors'} />
                <span className="font-bold text-[13px] tracking-tight">Cost Deck Health</span>
              </button>
            </div>
          </div>

          {/* Management Block */}
          <div>
            <div className="flex items-center gap-2 px-4 mb-4">
              <div className="h-px flex-1 bg-slate-800" />
              <h3 className="text-[10px] font-black uppercase tracking-widest text-slate-500 whitespace-nowrap">Management</h3>
              <div className="h-px flex-1 bg-slate-800" />
            </div>

            <div className="space-y-1.5">
              <button
                onClick={() => setActiveTab('scale')}
                className={`w-full flex items-center gap-3 px-4 py-3 rounded-xl transition-all duration-300 relative group overflow-hidden ${
                  activeTab === 'scale'
                    ? 'bg-emerald-500 shadow-lg shadow-emerald-500/20 text-white'
                    : 'text-slate-400 hover:text-white hover:bg-slate-800/50'
                }`}
              >
                {activeTab === 'scale' && <div className="absolute left-0 top-0 bottom-0 w-1 bg-white rounded-r-full shadow-[0_0_10px_white]" />}
                <Scaling size={20} className={activeTab === 'scale' ? 'text-white' : 'text-slate-500 group-hover:text-emerald-400 transition-colors'} />
                <span className="font-bold text-[13px] tracking-tight">Workload Scaling</span>
              </button>
            </div>
          </div>

          {/* Documentation Block */}
          <div>
            <div className="flex items-center gap-2 px-4 mb-4">
              <div className="h-px flex-1 bg-slate-800" />
              <h3 className="text-[10px] font-black uppercase tracking-widest text-slate-500 whitespace-nowrap">Documentation</h3>
              <div className="h-px flex-1 bg-slate-800" />
            </div>

            <div className="space-y-1.5">
              <button
                onClick={() => setActiveTab('api-docs')}
                className={`w-full flex items-center gap-3 px-4 py-3 rounded-xl transition-all duration-300 relative group overflow-hidden ${
                  activeTab === 'api-docs'
                    ? 'bg-emerald-500 shadow-lg shadow-emerald-500/20 text-white'
                    : 'text-slate-400 hover:text-white hover:bg-slate-800/50'
                }`}
              >
                {activeTab === 'api-docs' && <div className="absolute left-0 top-0 bottom-0 w-1 bg-white rounded-r-full shadow-[0_0_10px_white]" />}
                <BookOpen size={20} className={activeTab === 'api-docs' ? 'text-white' : 'text-slate-500 group-hover:text-emerald-400 transition-colors'} />
                <span className="font-bold text-[13px] tracking-tight">API Reference</span>
              </button>
            </div>
          </div>
        </nav>
        
        <div className="p-6 border-t border-slate-800">
          <button
            onClick={async () => {
              await fetch('/api/logout', { method: 'POST' })
              setIsAuthenticated(false)
            }}
            className="w-full flex items-center justify-center gap-2 px-4 py-2.5 rounded-xl text-slate-500 hover:text-red-400 hover:bg-red-500/10 transition-all text-xs font-bold"
          >
            <LogOut size={14} />
            Sign Out
          </button>
          <div className="text-xs text-slate-600 text-center mt-3">
            Cost Deck {appVersion}
          </div>
        </div>
      </aside>

      {/* Main Content Area */}
      <main className="flex-1 overflow-auto bg-slate-50">
        {activeTab === 'dashboard' && (
          selectedNamespace ? (
            <NamespaceDetails 
              namespace={selectedNamespace} 
              onBack={() => setSelectedNamespace(null)} 
            />
          ) : (
            <Dashboard onSelectNamespace={setSelectedNamespace} />
          )
        )}
        {activeTab === 'cluster' && <ClusterDashboard />}
        {activeTab === 'operator' && <OperatorHealth />}
        {activeTab === 'api-docs' && <ApiReference />}
        {activeTab === 'scale' && (
          selectedScalingNS ? (
            <ScalingWorkloads 
              namespace={selectedScalingNS} 
              onBack={() => setSelectedScalingNS(null)} 
            />
          ) : (
            <ScalingPage onSelectNamespace={setSelectedScalingNS} />
          )
        )}
      </main>
    </div>
  )
}

export default App

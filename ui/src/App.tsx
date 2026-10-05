import { useState, useEffect, useCallback } from 'react'
import { Scaling, Server, LineChart, Activity, BookOpen, FileText, LogOut, Settings, UserCircle2 } from 'lucide-react'
import type { ReactNode } from 'react'
import Dashboard from './pages/Dashboard'
import NamespaceDetails from './pages/NamespaceDetails'
import OperatorHealth from './pages/OperatorHealth'
import ScalingPage from './pages/ScalingPage'
import ClusterDashboard from './pages/ClusterDashboard'
import LoginPage from './pages/LoginPage'
import ApiReference from './pages/ApiReference'
import SettingsPage from './pages/SettingsPage'
import ReportsPage from './pages/ReportsPage'
import AIChatWidget from './components/AIChatModal'
import AuthCallback from './pages/AuthCallback'
import { AuthContext, type User } from './lib/auth'

interface Session {
  authenticated: boolean
  user: User | null
  version?: string
}

async function fetchSession(): Promise<Session> {
  try {
    const res = await fetch('/api/auth/me')
    if (res.status === 401) return { authenticated: false, user: null }
    const user: User | null = res.ok ? await res.json() : null
    const v = await fetch('/api/version').then(r => r.json()).catch(() => null)
    return { authenticated: true, user, version: v ? v.version || 'dev' : undefined }
  } catch {
    return { authenticated: false, user: null }
  }
}

function NavGroup({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <h3 className="px-3 mb-2 text-[10px] font-semibold uppercase tracking-widest text-slate-500">{title}</h3>
      <div className="space-y-1">{children}</div>
    </div>
  )
}

function NavItem({ icon, label, active, onClick }: { icon: ReactNode; label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className={`w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-colors ${
        active ? 'bg-brand-600 text-white' : 'text-slate-400 hover:text-white hover:bg-white/5'}`}
    >
      <span className={active ? 'text-white' : 'text-slate-500'}>{icon}</span>
      {label}
    </button>
  )
}

function App() {
  const [activeTab, setActiveTab] = useState<'dashboard' | 'scale' | 'cluster' | 'operator' | 'api-docs' | 'settings' | 'reports'>('dashboard')
  const [selectedNamespace, setSelectedNamespace] = useState<string | null>(null)
  const [selectedScalingNS, setSelectedScalingNS] = useState<string | null>(null)
  const [appVersion, setAppVersion] = useState('...')
  const [isAuthenticated, setIsAuthenticated] = useState<boolean | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const isSSOCallback = window.location.pathname === '/auth/callback'

  // applySession runs after the fetch resolves, never synchronously inside an effect.
  const applySession = useCallback((session: Session) => {
    setUser(session.user)
    setIsAuthenticated(session.authenticated)
    if (session.version) setAppVersion(session.version)
  }, [])

  const loadSession = useCallback(() => fetchSession().then(applySession), [applySession])

  useEffect(() => {
    if (!isSSOCallback) fetchSession().then(applySession)
  }, [isSSOCallback, applySession])

  const finishSSO = useCallback((returnTo: string) => {
    window.history.replaceState(null, '', returnTo)
    loadSession()
  }, [loadSession])

  if (isSSOCallback && isAuthenticated === null) {
    return <AuthCallback onSignedIn={finishSSO} />
  }

  // Loading state
  if (isAuthenticated === null) {
    return (
      <div className="min-h-screen bg-slate-900 flex items-center justify-center">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-brand-500"></div>
      </div>
    )
  }

  // Login gate
  if (isAuthenticated === false) {
    return <LoginPage onLogin={loadSession} />
  }

  const isAdmin = user?.role === 'admin'

  return (
    <AuthContext.Provider value={user}>
    <div className="flex h-screen w-full bg-slate-50 font-sans">
      {/* Sidebar */}
      <aside className="w-64 shrink-0 bg-slate-900 text-white flex flex-col z-10">
        <div className="p-6">
          <div className="flex items-center gap-4 mb-2">
            <div className="w-10 h-10 bg-brand-500 rounded-xl flex items-center justify-center">
              <svg xmlns="http://www.w3.org/2000/svg" width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" className="text-white">
                <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" />
                <polyline points="7 14 10 11 13 14 17 9" />
                <line x1="17" y1="9" x2="17" y2="13" />
                <line x1="17" y1="9" x2="13" y2="9" />
              </svg>
            </div>
          <div className="flex flex-col">
            <h1 className="text-xl font-bold tracking-tight text-white leading-none">Cost Deck</h1>
            <span className="text-[10px] uppercase tracking-[0.2em] text-brand-400 font-semibold mt-1">FinOps Platform</span>
          </div>
          </div>
        </div>

        <nav className="flex-1 px-4 mt-2 space-y-6 overflow-y-auto">
          <NavGroup title="Analytics">
            <NavItem icon={<LineChart size={18} />} label="Namespace Insights" active={activeTab === 'dashboard'} onClick={() => { setActiveTab('dashboard'); setSelectedNamespace(null) }} />
            <NavItem icon={<Server size={18} />} label="Cluster Node Map" active={activeTab === 'cluster'} onClick={() => setActiveTab('cluster')} />
            <NavItem icon={<Activity size={18} />} label="Cost Deck Health" active={activeTab === 'operator'} onClick={() => setActiveTab('operator')} />
          </NavGroup>
          <NavGroup title="Management">
            <NavItem icon={<Scaling size={18} />} label="Scaling Schedules" active={activeTab === 'scale'} onClick={() => { setActiveTab('scale'); setSelectedScalingNS(null) }} />
          </NavGroup>
          <NavGroup title="Reporting">
            <NavItem icon={<FileText size={18} />} label="AI Reports" active={activeTab === 'reports'} onClick={() => setActiveTab('reports')} />
          </NavGroup>
          <NavGroup title="Help">
            <NavItem icon={<BookOpen size={18} />} label="Documentation" active={activeTab === 'api-docs'} onClick={() => setActiveTab('api-docs')} />
          </NavGroup>
          {isAdmin && (
            <NavGroup title="Configuration">
              <NavItem icon={<Settings size={18} />} label="Settings" active={activeTab === 'settings'} onClick={() => setActiveTab('settings')} />
            </NavGroup>
          )}
        </nav>
        
        <div className="p-6 border-t border-slate-800">
          {user && user.provider !== 'anonymous' && (
            <div className="flex items-center gap-3 mb-4 px-1" title={user.email || user.name}>
              <UserCircle2 size={28} className="text-slate-500 shrink-0" />
              <div className="min-w-0">
                <div className="text-xs font-bold text-slate-200 truncate">{user.name}</div>
                <div className="text-[10px] uppercase tracking-widest text-brand-400 font-semibold">
                  {user.role}{user.provider === 'entra' ? ' · Microsoft' : ''}
                </div>
              </div>
            </div>
          )}
          <button
            onClick={async () => {
              await fetch('/api/logout', { method: 'POST' })
              setUser(null)
              setIsAuthenticated(false)
            }}
            className="w-full flex items-center justify-center gap-2 px-4 py-2.5 rounded-xl text-slate-500 hover:text-rose-400 hover:bg-rose-500/10 transition-all text-xs font-bold"
          >
            <LogOut size={14} />
            Sign Out
          </button>
          <div className="text-xs text-slate-600 text-center mt-3">
            {appVersion}
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
        {activeTab === 'settings' && isAdmin && <SettingsPage />}
        {activeTab === 'reports' && <ReportsPage />}
        {activeTab === 'scale' && (
          selectedScalingNS ? (
            <NamespaceDetails
              namespace={selectedScalingNS}
              onBack={() => setSelectedScalingNS(null)}
            />
          ) : (
            <ScalingPage onSelectNamespace={setSelectedScalingNS} />
          )
        )}
      </main>
      
      {/* AI Chat Widget */}
      <AIChatWidget />
    </div>
    </AuthContext.Provider>
  )
}

export default App

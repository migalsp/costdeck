import { useState, useEffect, useCallback, useRef } from 'react'
import { Scaling, Server, LineChart, Activity, BookOpen, FileText, LogOut, Settings, UserCircle2, LayoutDashboard, HardDrive, Target, KeyRound } from 'lucide-react'
import type { ReactNode } from 'react'
import NamespaceInsights from './pages/NamespaceInsights'
import Overview from './pages/Overview'
import StoragePage from './pages/StoragePage'
import BudgetsPage from './pages/BudgetsPage'
import NamespaceDetails from './pages/NamespaceDetails'
import OperatorHealth from './pages/OperatorHealth'
import ScalingPage from './pages/ScalingPage'
import ClusterDashboard from './pages/ClusterDashboard'
import LoginPage from './pages/LoginPage'
import Documentation from './pages/Documentation'
import SettingsPage from './pages/SettingsPage'
import ReportsPage from './pages/ReportsPage'
import AIChatWidget from './components/AIChatModal'
import AuthCallback from './pages/AuthCallback'
import ChangePasswordDialog from './components/ChangePasswordDialog'
import ThemeSwitch from './components/ThemeSwitch'
import Toaster from './components/Toaster'
import { AuthContext, isManagedUser, type User } from './lib/auth'
import { followLink, navigate, segment, splitPath, usePath } from './lib/router'
import { Button, PageHeader } from './components/ui'

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

// NavItem is a real link, so a page can be opened in a new tab or its address copied.
function NavItem({ icon, label, to, active }: { icon: ReactNode; label: string; to: string; active: boolean }) {
  return (
    <a
      href={to}
      onClick={e => followLink(e, to)}
      aria-current={active ? 'page' : undefined}
      className={`w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-colors ${
        active ? 'bg-brand-600 text-white' : 'text-slate-400 hover:text-white hover:bg-white/5'}`}
    >
      <span className={active ? 'text-white' : 'text-slate-500'}>{icon}</span>
      {label}
    </a>
  )
}

type Page = 'overview' | 'namespaces' | 'cluster' | 'storage' | 'health' | 'budgets' | 'scaling' | 'reports' | 'docs' | 'settings'

// Every page has one path. Namespace details live under the page they were opened from,
// /namespaces/<ns> or /scaling/<ns>, so Back leads to that list; the docs take a guide
// and the settings a section as their second segment.
const pages: Record<Page, { path: string; title: string; sub?: boolean }> = {
  overview: { path: '/', title: 'Cost Overview' },
  namespaces: { path: '/namespaces', title: 'Namespace Insights', sub: true },
  cluster: { path: '/cluster', title: 'Cluster Node Map' },
  storage: { path: '/storage', title: 'Storage & Network' },
  health: { path: '/health', title: 'Cost Deck Health' },
  budgets: { path: '/budgets', title: 'Budgets & Alerts' },
  scaling: { path: '/scaling', title: 'Scaling Schedules', sub: true },
  reports: { path: '/reports', title: 'Reports' },
  docs: { path: '/docs', title: 'Documentation', sub: true },
  settings: { path: '/settings', title: 'Settings', sub: true },
}

// The Overview links to other pages by these names.
const overviewTargets = { dashboard: 'namespaces', scale: 'scaling', cluster: 'cluster', storage: 'storage', budgets: 'budgets' } as const

// route reads the page from a path; page is null when nothing lives there.
function route(path: string): { page: Page | null; sub?: string } {
  const [first, sub, ...rest] = splitPath(path)
  if (!first) return { page: 'overview' }
  const page = (Object.keys(pages) as Page[]).find(p => p !== 'overview' && pages[p].path === `/${first}`)
  if (!page || rest.length > 0 || (sub && !pages[page].sub)) return { page: null }
  return { page, sub }
}

function NotFound({ title, subtitle }: { title: string; subtitle: string }) {
  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      <PageHeader title={title} subtitle={subtitle} />
      <Button variant="primary" onClick={() => navigate('/')}>Open the cost overview</Button>
    </div>
  )
}

function App() {
  const path = usePath()
  const { page, sub } = route(path)
  const main = useRef<HTMLElement>(null)
  const [appVersion, setAppVersion] = useState('...')
  const [isAuthenticated, setIsAuthenticated] = useState<boolean | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const [changingPassword, setChangingPassword] = useState(false)
  const isSSOCallback = path === '/auth/callback'

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

  // Leaving /auth/callback runs the session effect above, which loads the signed-in user.
  const finishSSO = useCallback((returnTo: string) => navigate(returnTo, { replace: true }), [])

  const nsTitle = sub && (page === 'namespaces' || page === 'scaling') ? sub : null
  const title = page ? nsTitle || pages[page].title : 'Page not found'
  useEffect(() => { document.title = `${title} · Cost Deck` }, [title])
  // A new page starts at the top, not where the previous one was scrolled to.
  useEffect(() => { main.current?.scrollTo({ top: 0 }) }, [path])

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
  const signOut = async () => {
    await fetch('/api/logout', { method: 'POST' })
    setUser(null)
    setIsAuthenticated(false)
  }

  // A password an administrator chose has to be replaced before anything else; the API
  // refuses every other call until then.
  if (user?.mustChangePassword) {
    return (
      <div className="min-h-screen bg-slate-900">
        <div className="theme-surface contents text-slate-900">
          <ChangePasswordDialog required onDone={loadSession} onClose={signOut} onSignOut={signOut} />
        </div>
      </div>
    )
  }

  return (
    <AuthContext.Provider value={user}>
    <div className="flex h-screen w-full bg-slate-50 font-sans">
      {/* Sidebar */}
      <aside className="w-64 shrink-0 bg-slate-900 text-white flex flex-col z-10">
        <div className="p-6">
          <div className="flex items-center gap-4 mb-2">
            <img src="/brand/cost-deck-icon-40.png" srcSet="/brand/cost-deck-icon-80.png 2x" width={40} height={40} alt="" className="shrink-0" />
          <div className="flex flex-col">
            <h1 className="text-xl font-bold tracking-tight text-white leading-none">Cost Deck</h1>
            <span className="text-[10px] uppercase tracking-[0.2em] text-brand-400 font-semibold mt-1">FinOps Platform</span>
          </div>
          </div>
        </div>

        <nav className="flex-1 px-4 mt-2 space-y-6 overflow-y-auto">
          <NavGroup title="Analytics">
            <NavItem icon={<LayoutDashboard size={18} />} label="Cost Overview" to="/" active={page === 'overview'} />
            <NavItem icon={<LineChart size={18} />} label="Namespace Insights" to="/namespaces" active={page === 'namespaces'} />
            <NavItem icon={<Server size={18} />} label="Cluster Node Map" to="/cluster" active={page === 'cluster'} />
            <NavItem icon={<HardDrive size={18} />} label="Storage & Network" to="/storage" active={page === 'storage'} />
            <NavItem icon={<Activity size={18} />} label="Cost Deck Health" to="/health" active={page === 'health'} />
          </NavGroup>
          <NavGroup title="Management">
            <NavItem icon={<Target size={18} />} label="Budgets & Alerts" to="/budgets" active={page === 'budgets'} />
            <NavItem icon={<Scaling size={18} />} label="Scaling Schedules" to="/scaling" active={page === 'scaling'} />
          </NavGroup>
          <NavGroup title="Reporting">
            <NavItem icon={<FileText size={18} />} label="Reports" to="/reports" active={page === 'reports'} />
          </NavGroup>
          <NavGroup title="Help">
            <NavItem icon={<BookOpen size={18} />} label="Documentation" to="/docs" active={page === 'docs'} />
          </NavGroup>
          {isAdmin && (
            <NavGroup title="Configuration">
              <NavItem icon={<Settings size={18} />} label="Settings" to="/settings" active={page === 'settings'} />
            </NavGroup>
          )}
        </nav>
        
        <div className="p-6 border-t border-slate-800">
          <ThemeSwitch />
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
          {isManagedUser(user) && (
            <button
              onClick={() => setChangingPassword(true)}
              className="w-full flex items-center justify-center gap-2 px-4 py-2 mb-1 rounded-xl text-slate-500 hover:text-white hover:bg-white/5 transition-all text-xs font-bold"
            >
              <KeyRound size={14} />
              Change password
            </button>
          )}
          <button
            onClick={signOut}
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

      {/* Everything right of the sidebar follows the theme (see .theme-surface in index.css). */}
      <div className="theme-surface contents text-slate-900">
      <main ref={main} className="flex-1 overflow-auto bg-slate-50">
        {page === 'overview' && (
          <Overview
            onSelectNamespace={name => navigate(`/namespaces/${segment(name)}`)}
            onNavigate={tab => navigate(pages[overviewTargets[tab]].path)}
          />
        )}
        {page === 'namespaces' && (
          sub ? (
            <NamespaceDetails key={sub} namespace={sub} onBack={() => navigate('/namespaces')} />
          ) : (
            <NamespaceInsights onSelectNamespace={name => navigate(`/namespaces/${segment(name)}`)} />
          )
        )}
        {page === 'cluster' && <ClusterDashboard />}
        {page === 'storage' && <StoragePage />}
        {page === 'budgets' && <BudgetsPage />}
        {page === 'health' && <OperatorHealth />}
        {page === 'docs' && <Documentation guide={sub} />}
        {page === 'settings' && (isAdmin
          ? <SettingsPage section={sub} />
          : <NotFound title="Settings" subtitle="Only administrators can change the settings." />)}
        {page === 'reports' && <ReportsPage />}
        {page === 'scaling' && (
          sub ? (
            <NamespaceDetails key={sub} namespace={sub} onBack={() => navigate('/scaling')} />
          ) : (
            <ScalingPage onSelectNamespace={name => navigate(`/scaling/${segment(name)}`)} />
          )
        )}
        {page === null && <NotFound title="Page not found" subtitle={`Nothing lives at ${path}.`} />}
      </main>
      
      {/* AI Chat Widget */}
      <AIChatWidget />
      {changingPassword && (
        <ChangePasswordDialog onDone={() => { setChangingPassword(false); loadSession() }} onClose={() => setChangingPassword(false)} onSignOut={signOut} />
      )}
      <Toaster />
      </div>
    </div>
    </AuthContext.Provider>
  )
}

export default App

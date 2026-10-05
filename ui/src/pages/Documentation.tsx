import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Activity, BookOpen, CalendarClock, Cloud, ExternalLink, FileCode2, LifeBuoy, MessageSquare, Plug, Rocket, Search, Shield, Sparkles, Wallet } from 'lucide-react'
import { guides, proseOf, textOf, type Guide } from './docs/content'
import { BlockView, Inline } from './docs/blocks'
import ApiExplorer from './docs/ApiExplorer'

const icons: Record<Guide['icon'], ReactNode> = {
  start: <Rocket size={17} />,
  schedule: <CalendarClock size={17} />,
  cloud: <Cloud size={17} />,
  cost: <Wallet size={17} />,
  access: <Shield size={17} />,
  ai: <Sparkles size={17} />,
  mcp: <Plug size={17} />,
  webex: <MessageSquare size={17} />,
  health: <Activity size={17} />,
  help: <LifeBuoy size={17} />,
}

const API = 'api'
const anchor = (guide: string, topic: string) => `doc-${guide}-${topic}`

interface Hit { guide: Guide; topicId: string; title: string; snippet: string }

// snippet cuts the text around the first match so results show why they matched.
function snippet(text: string, q: string): string {
  const plain = text.replace(/[`*]/g, '').replace(/\s+/g, ' ')
  const at = plain.toLowerCase().indexOf(q)
  if (at < 0) return plain.slice(0, 140)
  const start = Math.max(0, at - 60)
  return (start > 0 ? '…' : '') + plain.slice(start, at + 100) + (at + 100 < plain.length ? '…' : '')
}

export default function Documentation() {
  const [page, setPage] = useState<string>(guides[0].id)
  const [query, setQuery] = useState('')
  const [target, setTarget] = useState<string | null>(null)
  const main = useRef<HTMLDivElement>(null)

  const guide = guides.find(g => g.id === page)
  const q = query.trim().toLowerCase()

  const hits = useMemo<Hit[]>(() => {
    if (q.length < 2) return []
    const out: Hit[] = []
    for (const g of guides)
      for (const t of g.topics) {
        const text = [t.title, ...t.blocks.map(textOf)].join(' ')
        if (!text.toLowerCase().includes(q)) continue
        // Quote the prose where it matches; fall back to the examples.
        const prose = t.blocks.map(proseOf).join(' ')
        out.push({ guide: g, topicId: t.id, title: t.title, snippet: snippet(prose.toLowerCase().includes(q) ? prose : t.blocks.map(textOf).join(' '), q) })
      }
    return out
  }, [q])

  // Scroll to the requested topic once its guide is on screen, or to the top on a new page.
  useEffect(() => {
    if (target) document.getElementById(target)?.scrollIntoView({ block: 'start' })
    else main.current?.scrollTo({ top: 0 })
  }, [page, target])

  const open = (id: string, topic?: string) => {
    setPage(id)
    setQuery('')
    setTarget(topic ? anchor(id, topic) : null)
  }

  const order = [...guides.map(g => g.id), API]
  const at = order.indexOf(page)
  const title = (id: string) => (id === API ? 'REST API' : guides.find(g => g.id === id)?.title || '')

  const navItem = (id: string, label: string, icon: ReactNode) => (
    <button key={id} onClick={() => open(id)}
      className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-[13px] font-semibold transition-colors ${page === id && !q ? 'bg-brand-50 text-brand-700' : 'text-slate-600 hover:bg-slate-100 hover:text-slate-900'}`}>
      <span className={page === id && !q ? 'text-brand-600' : 'text-slate-400'}>{icon}</span>
      {label}
    </button>
  )

  return (
    <div className="flex h-full w-full bg-white">
      <aside className="w-64 shrink-0 border-r border-slate-200 bg-slate-50/60 flex flex-col h-full overflow-y-auto">
        <div className="p-5 pb-3">
          <h2 className="text-lg font-bold tracking-tight text-slate-900 flex items-center gap-2"><BookOpen className="text-brand-600" size={19} /> Documentation</h2>
          <div className="relative mt-3">
            <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
            <input value={query} onChange={e => setQuery(e.target.value)} placeholder="Search the guides"
              className="w-full pl-8 pr-2 py-1.5 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500" />
          </div>
        </div>
        <nav className="px-3 pb-6 space-y-5">
          <div>
            <h3 className="px-3 mb-1 text-[10px] font-bold uppercase tracking-widest text-slate-400">Guides</h3>
            <div className="space-y-0.5">{guides.map(g => navItem(g.id, g.title, icons[g.icon]))}</div>
          </div>
          <div>
            <h3 className="px-3 mb-1 text-[10px] font-bold uppercase tracking-widest text-slate-400">Reference</h3>
            <div className="space-y-0.5">
              {navItem(API, 'REST API', <FileCode2 size={17} />)}
              <a href="/api/docs" target="_blank" rel="noreferrer" className="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-[13px] font-semibold text-slate-600 hover:bg-slate-100 hover:text-slate-900">
                <ExternalLink size={17} className="text-slate-400" /> Swagger UI
              </a>
            </div>
          </div>
        </nav>
      </aside>

      <div ref={main} className="flex-1 h-full overflow-y-auto">
        <div className="max-w-3xl mx-auto px-10 py-10">
          {q ? (
            <div>
              <h1 className="text-xl font-bold text-slate-900 mb-1">Search</h1>
              <p className="text-sm text-slate-500 mb-6">{q.length < 2 ? 'Type at least two letters.' : `${hits.length} ${hits.length === 1 ? 'topic' : 'topics'} mention “${query.trim()}”.`}</p>
              <div className="space-y-2">
                {hits.map(h => (
                  <button key={h.guide.id + h.topicId} onClick={() => open(h.guide.id, h.topicId)}
                    className="w-full text-left p-4 rounded-xl border border-slate-200 hover:border-brand-300 hover:bg-brand-50/30 transition-colors">
                    <div className="text-xs font-semibold text-slate-400 flex items-center gap-1.5">{icons[h.guide.icon]} {h.guide.title}</div>
                    <div className="mt-1 font-semibold text-slate-800">{h.title}</div>
                    <div className="mt-1 text-sm text-slate-500">{h.snippet}</div>
                  </button>
                ))}
              </div>
            </div>
          ) : page === API ? (
            <ApiExplorer />
          ) : guide && (
            <article>
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 bg-brand-50 border border-brand-100 rounded-xl flex items-center justify-center text-brand-600">{icons[guide.icon]}</div>
                <h1 className="text-2xl font-bold tracking-tight text-slate-900">{guide.title}</h1>
              </div>
              <p className="mt-3 text-[15px] text-slate-500">{guide.summary}</p>

              {guide.topics.length > 2 && (
                <div className="mt-5 flex flex-wrap gap-1.5">
                  {guide.topics.map(t => (
                    <a key={t.id} href={`#${anchor(guide.id, t.id)}`}
                      onClick={e => { e.preventDefault(); document.getElementById(anchor(guide.id, t.id))?.scrollIntoView({ behavior: 'smooth', block: 'start' }) }}
                      className="px-2.5 py-1 rounded-lg border border-slate-200 text-xs font-medium text-slate-600 hover:border-brand-300 hover:text-brand-700">
                      {t.title}
                    </a>
                  ))}
                </div>
              )}

              {guide.topics.map(t => (
                <section key={t.id} id={anchor(guide.id, t.id)} className="mt-10 scroll-mt-6">
                  <h2 className="text-lg font-bold text-slate-900"><Inline text={t.title} /></h2>
                  {t.blocks.map((b, i) => <BlockView key={i} block={b} />)}
                </section>
              ))}

              <div className="mt-14 pt-6 border-t border-slate-100 flex justify-between text-sm font-semibold">
                {at > 0 ? <button onClick={() => open(order[at - 1])} className="text-brand-700 hover:text-brand-800">← {title(order[at - 1])}</button> : <span />}
                {at < order.length - 1 ? <button onClick={() => open(order[at + 1])} className="text-brand-700 hover:text-brand-800">{title(order[at + 1])} →</button> : <span />}
              </div>
            </article>
          )}
        </div>
      </div>
    </div>
  )
}

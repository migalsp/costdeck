import { Fragment, useState } from 'react'
import { AlertTriangle, Check, Copy, Lightbulb } from 'lucide-react'
import { Tabs } from '../../components/ui'
import type { Block } from './content'

// Inline renders `code` and **bold** inside guide text.
export function Inline({ text }: { text: string }) {
  return (
    <>
      {text.split(/(`[^`]+`|\*\*[^*]+\*\*)/g).map((part, i) => {
        if (part.startsWith('`') && part.endsWith('`') && part.length > 1)
          return <code key={i} className="px-1 py-0.5 rounded bg-slate-100 text-slate-800 font-mono text-[0.85em]">{part.slice(1, -1)}</code>
        if (part.startsWith('**') && part.endsWith('**') && part.length > 3)
          return <strong key={i} className="font-semibold text-slate-800">{part.slice(2, -2)}</strong>
        return <Fragment key={i}>{part}</Fragment>
      })}
    </>
  )
}

export function CodeBlock({ code, label }: { code: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard?.writeText(code).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    }).catch(() => {})
  }
  return (
    <div className="mt-3 rounded-xl overflow-hidden border border-slate-800 bg-slate-900">
      <div className="flex items-center justify-between px-3 py-1.5 border-b border-slate-800 text-[11px] text-slate-400">
        <span className="font-semibold">{label || ''}</span>
        <button onClick={copy} className="inline-flex items-center gap-1 hover:text-white transition-colors">
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <pre className="p-3 text-[12.5px] leading-relaxed font-mono text-slate-200 overflow-x-auto whitespace-pre">{code}</pre>
    </div>
  )
}

const wayLabels = { dashboard: 'Dashboard', api: 'API', kubectl: 'kubectl' } as const
type Way = keyof typeof wayLabels

function Ways({ ways }: { ways: Partial<Record<Way, string>> }) {
  const available = (Object.keys(wayLabels) as Way[]).filter(w => ways[w])
  const [way, setWay] = useState<Way>(available[0])
  if (available.length === 1) return <CodeBlock label={wayLabels[way]} code={ways[way]!} />
  return (
    <div className="mt-3">
      <Tabs value={way} onChange={setWay} tabs={available.map(w => ({ id: w, label: wayLabels[w] }))} />
      <CodeBlock code={ways[way]!} />
    </div>
  )
}

export function BlockView({ block }: { block: Block }) {
  if ('p' in block) return <p className="text-[15px] text-slate-600 leading-relaxed mt-3"><Inline text={block.p} /></p>
  if ('steps' in block)
    return (
      <ol className="mt-3 space-y-2">
        {block.steps.map((s, i) => (
          <li key={i} className="flex gap-3 text-[15px] text-slate-600 leading-relaxed">
            <span className="mt-0.5 w-6 h-6 shrink-0 rounded-full bg-brand-50 border border-brand-200 text-brand-700 text-xs font-bold flex items-center justify-center">{i + 1}</span>
            <span><Inline text={s} /></span>
          </li>
        ))}
      </ol>
    )
  if ('list' in block)
    return (
      <ul className="mt-3 space-y-1.5">
        {block.list.map((s, i) => (
          <li key={i} className="flex gap-2.5 text-[15px] text-slate-600 leading-relaxed">
            <span className="mt-2.5 w-1.5 h-1.5 shrink-0 rounded-full bg-slate-300" />
            <span><Inline text={s} /></span>
          </li>
        ))}
      </ul>
    )
  if ('tip' in block)
    return (
      <div className="mt-4 flex gap-2.5 p-3 rounded-xl border border-brand-200 bg-brand-50/60 text-sm text-slate-700 leading-relaxed">
        <Lightbulb size={16} className="mt-0.5 shrink-0 text-brand-600" />
        <span><Inline text={block.tip} /></span>
      </div>
    )
  if ('warn' in block)
    return (
      <div className="mt-4 flex gap-2.5 p-3 rounded-xl border border-amber-200 bg-amber-50 text-sm text-amber-900 leading-relaxed">
        <AlertTriangle size={16} className="mt-0.5 shrink-0 text-amber-600" />
        <span><Inline text={block.warn} /></span>
      </div>
    )
  if ('code' in block) return <CodeBlock code={block.code} label={block.label} />
  if ('table' in block)
    return (
      <div className="mt-3 overflow-x-auto rounded-xl border border-slate-200">
        <table className="w-full text-sm">
          <thead className="bg-slate-50">
            <tr>{block.table.head.map(h => <th key={h} className="text-left px-3 py-2 text-xs font-semibold text-slate-500">{h}</th>)}</tr>
          </thead>
          <tbody>
            {block.table.rows.map((row, i) => (
              <tr key={i} className="border-t border-slate-100 align-top">
                {row.map((cell, j) => (
                  <td key={j} className={`px-3 py-2 leading-relaxed ${j === 0 ? 'font-medium text-slate-800 whitespace-nowrap' : 'text-slate-600'}`}><Inline text={cell} /></td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    )
  return <Ways ways={block.ways} />
}

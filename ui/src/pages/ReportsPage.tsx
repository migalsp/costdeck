import { useState, useEffect } from 'react'
import { FileText, Download, Loader2, RefreshCw } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import { apiError } from '../lib/api'
import { useAuth } from '../lib/auth'
import remarkGfm from 'remark-gfm'
import { Button } from '../components/ui'

export default function ReportsPage() {
  const { can } = useAuth()
  const [report, setReport] = useState<string>('')
  const [isGenerating, setIsGenerating] = useState(false)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [generatedAt, setGeneratedAt] = useState<string | null>(null)

  useEffect(() => {
    fetch('/api/ai/report')
      .then(r => r.json())
      .then(d => {
        if (d.report) {
          setReport(d.report)
        }
        if (d.generatedAt) setGeneratedAt(d.generatedAt)
        setIsLoading(false)
      })
      .catch(e => {
        console.error('Failed to load report:', e)
        setIsLoading(false)
      })
  }, [])

  const handleGenerate = async () => {
    setIsGenerating(true)
    setError(null)
    setReport('')

    try {
      const response = await fetch('/api/ai/report/generate', { method: 'POST' })
      if (!response.ok) {
        throw new Error(await apiError(response))
      }

      const reader = response.body?.getReader()
      if (!reader) throw new Error('Streaming is not supported by this browser')

      const decoder = new TextDecoder()
      let buffer = ''
      let fullReport = ''
      let streamError: string | null = null

      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        let sep
        while ((sep = buffer.indexOf('\n\n')) !== -1) {
          const frame = buffer.slice(0, sep)
          buffer = buffer.slice(sep + 2)
          for (const line of frame.split('\n')) {
            if (!line.startsWith('data:')) continue
            try {
              const ev = JSON.parse(line.slice(5).trim())
              if (ev.type === 'text') {
                fullReport += ev.text
                setReport(fullReport)
              } else if (ev.type === 'reset') {
                fullReport = ''
                setReport('')
              } else if (ev.type === 'error') {
                streamError = ev.text
              }
            } catch {
              // Ignore keep-alives and malformed frames.
            }
          }
        }
      }

      if (streamError) throw new Error(streamError)
      if (!fullReport.trim()) {
        throw new Error('The AI provider returned an empty report. Check Settings → AI Models and the operator logs.')
      }
      // The server saves the finished report itself.
      setGeneratedAt(new Date().toISOString())
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setIsGenerating(false)
    }
  }

  const handleExportPDF = () => {
    window.print()
  }

  return (
    <div className="p-8 max-w-[1200px] mx-auto w-full print-page">
      <div className="flex items-center justify-between mb-8 print-hide">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-slate-900 flex items-center gap-2">
            AI Cost & Health Reports
          </h1>
          <p className="mt-1 text-sm text-slate-500">
            AI-driven insights &mdash; Discover hidden bottlenecks, eliminate waste
          </p>
          {generatedAt && (
            <p className="text-xs text-slate-400 mt-1">Last generated {new Date(generatedAt).toLocaleString()}</p>
          )}
        </div>

        <div className="flex gap-3">
          {can('operator') && <Button
            variant="primary"
            onClick={handleGenerate}
            disabled={isGenerating}
            icon={isGenerating ? <Loader2 size={16} className="animate-spin" /> : <RefreshCw size={16} />}
          >
            {isGenerating ? 'Generating…' : 'Generate new report'}
          </Button>}
          
          <Button
            onClick={handleExportPDF}
            disabled={isGenerating || !report}
            icon={<Download size={16} />}
          >
            Export PDF
          </Button>
        </div>
      </div>

      {error && (
        <div className="mb-6 p-4 bg-rose-50 border border-rose-100 rounded-xl text-sm font-medium text-rose-600 print-hide">{error}</div>
      )}

      <div className="bg-white rounded-xl shadow-xl border border-slate-100 min-h-[600px] print-container">
        {isLoading ? (
          <div className="flex items-center justify-center h-[600px]">
            <Loader2 size={32} className="animate-spin text-emerald-500" />
          </div>
        ) : report ? (
          <div className="prose prose-slate prose-emerald max-w-none p-10 print-prose">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>
              {report}
            </ReactMarkdown>
          </div>
        ) : (
          <div className="flex flex-col items-center justify-center h-[600px] text-slate-400">
            <FileText size={64} className="mb-4 opacity-20" />
            <p className="font-medium text-lg text-slate-600">No reports have been generated yet.</p>
            <p className="text-sm mt-1 text-slate-500">Click "Generate new report" to start your first deep-dive FinOps analysis.</p>
          </div>
        )}
      </div>
    </div>
  )
}

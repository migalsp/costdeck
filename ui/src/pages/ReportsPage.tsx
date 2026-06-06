import { useState, useEffect } from 'react'
import { FileText, Download, Loader2, RefreshCw } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

export default function ReportsPage() {
  const [report, setReport] = useState<string>('')
  const [isGenerating, setIsGenerating] = useState(false)
  const [isLoading, setIsLoading] = useState(true)

  useEffect(() => {
    fetch('/api/ai/report')
      .then(r => r.json())
      .then(d => {
        if (d.report) {
          setReport(d.report)
        }
        setIsLoading(false)
      })
      .catch(e => {
        console.error('Failed to load report:', e)
        setIsLoading(false)
      })
  }, [])

  const handleGenerate = async () => {
    setIsGenerating(true)
    setReport('')

    try {
      const response = await fetch('/api/ai/report/generate', {
        method: 'POST'
      })

      if (!response.ok) {
        throw new Error('Failed to generate report')
      }

      const reader = response.body?.getReader()
      if (!reader) throw new Error('No reader available')

      const decoder = new TextDecoder()
      let fullReport = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break

        const chunk = decoder.decode(value, { stream: true })
        const lines = chunk.split('\n')
        
        for (const line of lines) {
          if (line.startsWith('data: ')) {
            const dataStr = line.slice(6)
            if (dataStr === '[DONE]') continue
            try {
              const textChunk = JSON.parse(dataStr)
              fullReport += textChunk
              setReport(fullReport)
            } catch (e) {
              console.error('Failed to parse stream chunk:', e)
            }
          }
        }
      }

      if (!fullReport.trim()) {
        throw new Error('Received empty response from the AI server. Please check the operator logs.')
      }

      // Save the generated report
      await fetch('/api/ai/report/save', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ report: fullReport })
      })

    } catch (e) {
      console.error(e)
      setReport('**Error generating report.** Please make sure the AI integration is enabled in Settings and valid credentials are provided.')
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
          <h1 className="text-3xl font-black tracking-tight text-slate-900 flex items-center gap-3">
            <FileText className="text-emerald-500" size={32} />
            AI Cost & Health Reports
          </h1>
          <p className="text-slate-500 mt-2 font-medium">
            AI-driven insights &mdash; Discover hidden bottlenecks, eliminate waste
          </p>
        </div>

        <div className="flex gap-3">
          <button
            onClick={handleGenerate}
            disabled={isGenerating}
            className="flex items-center gap-2 bg-emerald-500 text-white px-5 py-2.5 rounded-xl font-bold shadow-lg shadow-emerald-500/20 hover:bg-emerald-600 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {isGenerating ? <Loader2 size={18} className="animate-spin" /> : <RefreshCw size={18} />}
            {isGenerating ? 'Generating...' : 'Generate New Report'}
          </button>
          
          <button
            onClick={handleExportPDF}
            disabled={isGenerating || !report}
            className="flex items-center gap-2 bg-white text-slate-700 border-2 border-slate-200 px-5 py-2.5 rounded-xl font-bold hover:bg-slate-50 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Download size={18} />
            Export PDF
          </button>
        </div>
      </div>

      <div className="bg-white rounded-2xl shadow-xl shadow-slate-200/50 border border-slate-100 min-h-[600px] print-container">
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
            <p className="text-sm mt-1 text-slate-500">Click "Generate New Report" to start your first deep-dive FinOps analysis.</p>
          </div>
        )}
      </div>
    </div>
  )
}

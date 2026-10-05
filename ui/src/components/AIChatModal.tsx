import { useState, useRef, useEffect } from 'react';
import { X, Send, Loader2, User, Wrench, CheckCircle2, XCircle, ShieldCheck, Sparkles, Square } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { useAuth } from '../lib/auth';
import { apiError } from '../lib/api';

// Events streamed by POST /api/ai/chat (server-sent events, one JSON object per event).
interface AgentEvent {
  type: 'text' | 'reset' | 'tool_call' | 'tool_result' | 'confirm' | 'error' | 'done';
  text?: string;
  tool?: string;
  args?: Record<string, unknown>;
  isError?: boolean;
}

interface Activity { tool: string; result?: string; isError?: boolean }

interface Confirmation {
  id: string;
  tool: string;
  args: Record<string, unknown>;
  status: 'pending' | 'running' | 'done' | 'failed' | 'dismissed';
  result?: string;
}

interface ChatItem {
  id: string;
  role: 'user' | 'assistant';
  text: string;
  activity: Activity[];
  confirmations: Confirmation[];
  error?: string;
}

const TOOL_LABELS: Record<string, string> = {
  get_cluster_overview: 'Reading the cluster overview',
  list_scaling_groups: 'Listing scaling groups',
  get_scaling_group: 'Inspecting a scaling group',
  list_namespace_configs: 'Listing namespace configs',
  list_namespaces: 'Ranking namespaces by cost',
  get_namespace_status: 'Inspecting a namespace',
  get_rightsizing_recommendations: 'Checking what can be reduced',
  scale_group: 'Scale group',
  scale_namespace: 'Scale namespace',
  revert_optimization: 'Revert right-sizing',
};

// describeAction renders a pending action as a sentence for the confirmation card.
function describeAction(tool: string, args: Record<string, unknown>): string {
  const target = String(args.name ?? args.namespace ?? '');
  const action = String(args.action ?? '');
  const duration = args.duration ? String(args.duration) : '';
  const hold = duration === 'forever' ? ' until resumed'
    : duration && duration !== 'nextTransition' ? ` for ${duration}`
    : action !== 'resume' ? ' until the next scheduled change' : '';
  switch (tool) {
    case 'scale_group':
    case 'scale_namespace':
      return action === 'resume'
        ? `Hand ${target} back to its schedule`
        : `Force ${target} ${action}${hold}`;
    case 'revert_optimization':
      return `Restore the original requests and limits in ${target}`;
    default:
      return `${tool} ${JSON.stringify(args)}`;
  }
}

// readEvents parses a server-sent event stream into AgentEvents.
async function readEvents(res: Response, onEvent: (e: AgentEvent) => void) {
  const reader = res.body?.getReader();
  if (!reader) return;
  const decoder = new TextDecoder();
  let buffer = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let sep;
    while ((sep = buffer.indexOf('\n\n')) !== -1) {
      const frame = buffer.slice(0, sep);
      buffer = buffer.slice(sep + 2);
      for (const line of frame.split('\n')) {
        if (!line.startsWith('data:')) continue;
        try {
          onEvent(JSON.parse(line.slice(5).trim()));
        } catch {
          // Ignore keep-alives and malformed frames.
        }
      }
    }
  }
}

export default function AIChatWidget() {
  const { can } = useAuth();
  const [isOpen, setIsOpen] = useState(false);
  const [isAIConfigured, setIsAIConfigured] = useState(false);
  const [input, setInput] = useState('');
  const [items, setItems] = useState<ChatItem[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    fetch('/api/settings')
      .then(res => (res.ok ? res.json() : null))
      .then(data => setIsAIConfigured(!!data?.integrations?.ai?.enabled))
      .catch(() => setIsAIConfigured(false));
  }, []);

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [items, isLoading]);

  const updateItem = (id: string, fn: (item: ChatItem) => ChatItem) =>
    setItems(prev => prev.map(it => (it.id === id ? fn(it) : it)));

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const text = input.trim();
    if (!text || isLoading) return;
    setInput('');

    const userItem: ChatItem = { id: crypto.randomUUID(), role: 'user', text, activity: [], confirmations: [] };
    const assistantId = crypto.randomUUID();
    const history = [...items, userItem]
      .filter(it => it.text.trim())
      .map(it => ({ role: it.role, content: it.text }));
    setItems(prev => [...prev, userItem, { id: assistantId, role: 'assistant', text: '', activity: [], confirmations: [] }]);
    setIsLoading(true);

    const controller = new AbortController();
    abortRef.current = controller;
    try {
      const res = await fetch('/api/ai/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ messages: history }),
        signal: controller.signal,
      });
      if (!res.ok) throw new Error(await apiError(res));

      await readEvents(res, ev => {
        switch (ev.type) {
          case 'text':
            updateItem(assistantId, it => ({ ...it, text: it.text + (ev.text || '') }));
            break;
          case 'reset':
            updateItem(assistantId, it => ({ ...it, text: '' }));
            break;
          case 'tool_call':
            updateItem(assistantId, it => ({ ...it, activity: [...it.activity, { tool: ev.tool || '' }] }));
            break;
          case 'tool_result':
            updateItem(assistantId, it => {
              const activity = [...it.activity];
              const idx = activity.map(a => a.tool).lastIndexOf(ev.tool || '');
              if (idx >= 0) activity[idx] = { ...activity[idx], result: ev.text, isError: ev.isError };
              else activity.push({ tool: ev.tool || '', result: ev.text, isError: ev.isError });
              return { ...it, activity };
            });
            break;
          case 'confirm':
            updateItem(assistantId, it => ({
              ...it,
              activity: it.activity.filter(a => a.tool !== ev.tool || a.result !== undefined),
              confirmations: [...it.confirmations, { id: crypto.randomUUID(), tool: ev.tool || '', args: ev.args || {}, status: 'pending' }],
            }));
            break;
          case 'error':
            updateItem(assistantId, it => ({ ...it, error: ev.text }));
            break;
        }
      });
    } catch (err) {
      if ((err as Error).name !== 'AbortError') {
        updateItem(assistantId, it => ({ ...it, error: (err as Error).message }));
      }
    } finally {
      abortRef.current = null;
      setIsLoading(false);
    }
  };

  const runConfirmation = async (itemId: string, c: Confirmation) => {
    const setStatus = (patch: Partial<Confirmation>) =>
      updateItem(itemId, it => ({ ...it, confirmations: it.confirmations.map(x => (x.id === c.id ? { ...x, ...patch } : x)) }));
    setStatus({ status: 'running' });
    try {
      const res = await fetch(`/api/ai/tools/${c.tool}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ args: c.args }),
      });
      if (!res.ok) throw new Error(await apiError(res));
      const data = await res.json();
      setStatus({ status: 'done', result: data.result });
      // Record the outcome in the conversation so the next turn knows it happened.
      updateItem(itemId, it => ({ ...it, text: `${it.text}\n\n✅ ${data.result}` }));
    } catch (err) {
      setStatus({ status: 'failed', result: (err as Error).message });
    }
  };

  if (!isAIConfigured) {
    return null;
  }

  return (
    <>
      <button
        onClick={() => setIsOpen(true)}
        className={`fixed bottom-8 right-8 w-14 h-14 bg-brand-600 hover:bg-brand-700 text-white rounded-xl shadow-lg flex items-center justify-center transition-all duration-300 hover:scale-105 z-50 ring-1 ring-white/20 ${isOpen ? 'scale-0 opacity-0 pointer-events-none' : 'scale-100 opacity-100'}`}
        aria-label="Open CostDeck AI"
      >
        <Sparkles size={24} />
      </button>

      <div
        className={`fixed bottom-8 right-8 w-[520px] max-w-[calc(100vw-2rem)] h-[720px] max-h-[calc(100vh-4rem)] bg-white rounded-xl shadow-2xl flex flex-col overflow-hidden transition-all duration-300 z-50 border border-slate-200 transform origin-bottom-right ${isOpen ? 'scale-100 opacity-100' : 'scale-75 opacity-0 pointer-events-none'}`}
      >
        <div className="bg-brand-600 p-4 flex items-center justify-between text-white shrink-0 shadow-sm z-10 border-b border-brand-700">
          <div className="flex items-center gap-3">
            <div className="w-8 h-8 bg-white/20 rounded-lg flex items-center justify-center border border-white/20 shadow-sm">
              <Sparkles size={18} className="text-white" />
            </div>
            <div>
              <h3 className="font-semibold text-sm tracking-tight text-white">CostDeck AI</h3>
              <p className="text-[11px] text-emerald-50 font-medium tracking-wide">
                {can('operator') ? 'Answers questions · proposes actions you confirm' : 'Read-only assistant'}
              </p>
            </div>
          </div>
          <div className="flex items-center gap-1">
            {items.length > 0 && !isLoading && (
              <button onClick={() => setItems([])} className="px-2 py-1 text-[11px] font-bold text-emerald-50 hover:bg-white/20 rounded-lg">New chat</button>
            )}
            <button onClick={() => setIsOpen(false)} className="p-2 hover:bg-white/20 text-emerald-50 hover:text-white rounded-lg transition-colors" aria-label="Close">
              <X size={18} />
            </button>
          </div>
        </div>

        <div className="flex-1 overflow-y-auto bg-slate-50 min-h-0 flex flex-col">
          {items.length === 0 && (
            <div className="h-full flex flex-col items-center justify-center text-center space-y-4 p-8">
              <img src="/brand/cost-deck-icon-64.png" srcSet="/brand/cost-deck-icon-128.png 2x" width={56} height={56} alt="" />
              <p className="text-sm font-medium text-slate-500 leading-relaxed max-w-[80%]">
                Ask about costs, waste, or schedules.
              </p>
              <div className="flex flex-wrap gap-2 justify-center max-w-[90%]">
                {['Which namespaces waste the most money?', 'Why is my group still up?', 'What scales down tonight?'].map(q => (
                  <button key={q} onClick={() => setInput(q)} className="px-3 py-1.5 text-[11px] font-bold text-slate-600 bg-white border border-slate-200 rounded-full hover:border-emerald-300">
                    {q}
                  </button>
                ))}
              </div>
            </div>
          )}

          {items.map(item => (
            <div key={item.id} className={`flex flex-col w-full py-4 px-6 gap-2 border-b border-slate-100 ${item.role === 'user' ? 'bg-white' : 'bg-slate-50/80'}`}>
              <div className="flex items-center gap-1.5 text-[10px] font-bold text-slate-400 uppercase tracking-wider">
                {item.role === 'user' ? (<><User size={12} /> You</>) : (<><Sparkles size={12} className="text-emerald-500" /> CostDeck AI</>)}
              </div>

              {item.activity.length > 0 && (
                <div className="flex flex-col gap-1">
                  {item.activity.map((a, i) => (
                    <div key={i} className={`flex items-start gap-1.5 text-[11px] ${a.isError ? 'text-rose-500' : 'text-slate-400'}`} title={a.result}>
                      {a.result === undefined ? <Loader2 size={12} className="animate-spin mt-0.5 shrink-0" /> : a.isError ? <XCircle size={12} className="mt-0.5 shrink-0" /> : <Wrench size={12} className="mt-0.5 shrink-0" />}
                      <span>{TOOL_LABELS[a.tool] || a.tool}{a.isError && a.result ? ` — ${a.result}` : ''}</span>
                    </div>
                  ))}
                </div>
              )}

              {item.text && (
                <div className="prose prose-sm prose-slate max-w-none text-slate-700 leading-relaxed prose-p:my-1 prose-pre:my-2 prose-pre:bg-slate-900 prose-pre:text-slate-50 prose-th:bg-slate-100 prose-th:p-2 prose-td:p-2 prose-table:border-collapse prose-table:w-full prose-table:border prose-table:border-slate-200">
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>{item.text}</ReactMarkdown>
                </div>
              )}

              {item.confirmations.map(c => (
                <div key={c.id} className="mt-1 p-3 rounded-xl border border-amber-200 bg-amber-50">
                  <div className="flex items-center gap-2 text-xs font-bold text-amber-800">
                    <ShieldCheck size={14} /> {describeAction(c.tool, c.args)}
                  </div>
                  {c.status === 'pending' && (
                    <div className="flex gap-2 mt-2">
                      <button onClick={() => runConfirmation(item.id, c)} className="px-3 py-1.5 bg-amber-500 hover:bg-amber-600 text-white text-xs font-bold rounded-lg">Confirm</button>
                      <button onClick={() => updateItem(item.id, it => ({ ...it, confirmations: it.confirmations.map(x => x.id === c.id ? { ...x, status: 'dismissed' } : x) }))}
                        className="px-3 py-1.5 bg-white border border-amber-200 text-amber-700 text-xs font-bold rounded-lg">Dismiss</button>
                    </div>
                  )}
                  {c.status === 'running' && <div className="flex items-center gap-1.5 mt-2 text-xs text-amber-700"><Loader2 size={12} className="animate-spin" /> Applying…</div>}
                  {c.status === 'done' && <div className="flex items-center gap-1.5 mt-2 text-xs text-emerald-700"><CheckCircle2 size={12} /> {c.result}</div>}
                  {c.status === 'failed' && <div className="flex items-center gap-1.5 mt-2 text-xs text-rose-600"><XCircle size={12} /> {c.result}</div>}
                  {c.status === 'dismissed' && <div className="mt-2 text-xs text-slate-400">Dismissed — nothing was changed.</div>}
                </div>
              ))}

              {item.error && (
                <div className="mt-1 p-3 rounded-xl border border-rose-200 bg-rose-50 text-xs text-rose-600 font-medium">{item.error}</div>
              )}

              {item.role === 'assistant' && isLoading && item.id === items[items.length - 1]?.id && !item.text && item.activity.length === 0 && (
                <div className="flex items-center gap-2 mt-1">
                  <Loader2 size={16} className="text-emerald-500 animate-spin" />
                  <span className="text-xs font-medium text-slate-500">Thinking…</span>
                </div>
              )}
            </div>
          ))}
          <div ref={messagesEndRef} />
        </div>

        <form onSubmit={handleSubmit} className="p-4 bg-white border-t border-slate-200 shrink-0">
          <div className="relative flex items-center">
            <input
              type="text"
              value={input}
              onChange={e => setInput(e.target.value)}
              placeholder="Message CostDeck AI..."
              className="w-full pl-4 pr-12 py-3.5 bg-slate-50 hover:bg-slate-100/80 focus:bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-emerald-500/20 focus:border-emerald-500 transition-all font-medium text-slate-800 placeholder-slate-400 shadow-sm"
            />
            {isLoading ? (
              <button type="button" onClick={() => abortRef.current?.abort()} className="absolute right-2.5 p-2 rounded-lg bg-slate-200 text-slate-600 hover:bg-slate-300" aria-label="Stop">
                <Square size={16} />
              </button>
            ) : (
              <button type="submit" disabled={!input.trim()}
                className={`absolute right-2.5 p-2 rounded-lg transition-all ${input.trim() ? 'bg-brand-600 text-white hover:bg-brand-700 shadow-sm' : 'text-slate-300 bg-transparent'}`}
                aria-label="Send">
                <Send size={16} />
              </button>
            )}
          </div>
        </form>
      </div>
    </>
  );
}

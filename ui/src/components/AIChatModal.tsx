import { useState, useRef, useEffect } from 'react';
import { X, Send, Loader2, User } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

interface Message {
  id: string;
  role: 'user' | 'assistant';
  content: string;
}

const CostDeckLogo = ({ size = 24, className = "" }: { size?: number, className?: string }) => (
  <svg xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" className={className}>
    <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" />
    <polyline points="7 14 10 11 13 14 17 9" />
    <line x1="17" y1="9" x2="17" y2="13" />
    <line x1="17" y1="9" x2="13" y2="9" />
  </svg>
);

export default function AIChatWidget() {
  const [isOpen, setIsOpen] = useState(false);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const [isAIConfigured, setIsAIConfigured] = useState(false);

  const [input, setInput] = useState('');
  const [messages, setMessages] = useState<Message[]>([]);
  const [isLoading, setIsLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!input.trim() || isLoading) return;

    const userMsg = input.trim();
    setInput('');
    setMessages(prev => [...prev, { id: Date.now().toString(), role: 'user', content: userMsg }]);
    setIsLoading(true);

    const assistantMsgId = (Date.now() + 1).toString();
    setMessages(prev => [...prev, { id: assistantMsgId, role: 'assistant', content: '' }]);

    try {
      const historyForBackend = messages.map(m => ({ role: m.role, content: m.content }));
      historyForBackend.push({ role: 'user', content: userMsg });

      const response = await fetch('/api/ai/chat', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({ prompt: userMsg, messages: historyForBackend })
      });

      if (!response.ok) {
        throw new Error('Failed to get AI response');
      }

      const reader = response.body?.getReader();
      const decoder = new TextDecoder();
      let assistantContent = '';

      if (reader) {
        while (true) {
          const { value, done } = await reader.read();
          if (done) break;

          const chunk = decoder.decode(value, { stream: true });
          const lines = chunk.split('\n');

          for (const line of lines) {
            if (line.startsWith('0:')) {
              try {
                const textDelta = JSON.parse(line.substring(2));
                assistantContent += textDelta;

                setMessages(prev => {
                  const newMessages = [...prev];
                  const idx = newMessages.findIndex(m => m.id === assistantMsgId);
                  if (idx !== -1) {
                    newMessages[idx].content = assistantContent;
                  }
                  return newMessages;
                });
              } catch (e) {
                // Ignore partial JSON parsing errors
              }
            }
          }
        }
      }
    } catch (err: any) {
      setMessages(prev => {
        const newMessages = [...prev];
        const idx = newMessages.findIndex(m => m.id === assistantMsgId);
        if (idx !== -1) {
          newMessages[idx].content = `Error: ${err.message}`;
        }
        return newMessages;
      });
    } finally {
      setIsLoading(false);
    }
  };

  const fetchSettings = async () => {
    try {
      const res = await fetch('/api/settings');
      if (res.ok) {
        const data = await res.json();
        if (data?.integrations?.ai?.enabled) {
          setIsAIConfigured(true);
        } else {
          setIsAIConfigured(false);
        }
      }
    } catch {
      setIsAIConfigured(false);
    }
  };

  useEffect(() => {
    fetchSettings();
  }, []);

  const scrollToBottom = () => {
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
  };

  useEffect(() => {
    scrollToBottom();
  }, [messages, isLoading]);

  if (!isAIConfigured) {
    return null;
  }

  return (
    <>
      {/* Floating Action Button */}
      <button
        onClick={() => setIsOpen(true)}
        className={`fixed bottom-8 right-8 w-14 h-14 bg-emerald-500 hover:bg-emerald-600 text-white rounded-2xl shadow-lg shadow-emerald-500/30 flex items-center justify-center transition-all duration-300 hover:scale-105 z-50 ring-1 ring-white/20 ${isOpen ? 'scale-0 opacity-0 pointer-events-none' : 'scale-100 opacity-100'}`}
      >
        <CostDeckLogo size={24} />
      </button>

      {/* Chat Window */}
      <div
        className={`fixed bottom-8 right-8 w-[500px] h-[700px] bg-white rounded-xl shadow-2xl flex flex-col overflow-hidden transition-all duration-300 z-50 border border-slate-200 transform origin-bottom-right ${isOpen ? 'scale-100 opacity-100' : 'scale-75 opacity-0 pointer-events-none'}`}
      >
        {/* Header */}
        <div className="bg-emerald-500 p-4 flex items-center justify-between text-white shrink-0 shadow-sm z-10 border-b border-emerald-600">
          <div className="flex items-center gap-3">
            <div className="w-8 h-8 bg-white/20 rounded-lg flex items-center justify-center border border-white/20 shadow-sm">
              <CostDeckLogo size={18} className="text-white" />
            </div>
            <div>
              <h3 className="font-semibold text-sm tracking-tight text-white">CostDeck AI</h3>
              <p className="text-[11px] text-emerald-50 font-medium tracking-wide">FinOps Assistant</p>
            </div>
          </div>
          <button
            onClick={() => setIsOpen(false)}
            className="p-2 hover:bg-white/20 text-emerald-50 hover:text-white rounded-lg transition-colors"
          >
            <X size={18} />
          </button>
        </div>

        {/* Messages */}
        <div className="flex-1 overflow-y-auto bg-slate-50 min-h-0 flex flex-col">
          {messages.length === 0 && (
            <div className="h-full flex flex-col items-center justify-center text-center opacity-60 space-y-4 p-8">
              <div className="w-16 h-16 bg-emerald-100/50 rounded-full flex items-center justify-center border border-emerald-200/50">
                <CostDeckLogo size={32} className="text-emerald-500" />
              </div>
              <p className="text-sm font-medium text-slate-500 leading-relaxed max-w-[80%]">
                Ask me about cluster costs, noisy namespaces, or scaling optimizations!
              </p>
            </div>
          )}

          {messages.map((msg: Message) => (
            <div key={msg.id} className={`flex flex-col w-full py-4 px-6 gap-2 border-b border-slate-100 ${msg.role === 'user' ? 'bg-white' : 'bg-slate-50/80'}`}>
              <div className="flex items-center gap-1.5 text-[10px] font-bold text-slate-400 uppercase tracking-wider">
                {msg.role === 'user' ? (
                  <>
                    <User size={12} /> You
                  </>
                ) : (
                  <>
                    <CostDeckLogo size={12} className="text-emerald-500" /> CostDeck AI
                  </>
                )}
              </div>
              <div className="w-full min-w-0">
                <div className="prose prose-sm prose-slate max-w-none text-slate-700 leading-relaxed prose-p:my-1 prose-pre:my-2 prose-pre:bg-slate-900 prose-pre:text-slate-50 prose-th:bg-slate-100 prose-th:p-2 prose-td:p-2 prose-table:border-collapse prose-table:w-full prose-table:border prose-table:border-slate-200">
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg.content}</ReactMarkdown>
                </div>
              </div>
            </div>
          ))}

          {isLoading && messages.length > 0 && messages[messages.length - 1].role === 'user' && (
            <div className="flex flex-col w-full py-4 px-6 gap-2 bg-slate-50/80 border-b border-slate-100">
              <div className="flex items-center gap-1.5 text-[10px] font-bold text-slate-400 uppercase tracking-wider">
                <CostDeckLogo size={12} className="text-emerald-500" /> CostDeck AI
              </div>
              <div className="flex items-center gap-2 mt-1">
                <Loader2 size={16} className="text-emerald-500 animate-spin" />
                <span className="text-xs font-medium text-slate-500">Processing...</span>
              </div>
            </div>
          )}
          <div ref={messagesEndRef} />
        </div>

        {/* Input area */}
        <form onSubmit={handleSubmit} className="p-4 bg-white border-t border-slate-200 shrink-0">
          <div className="relative flex items-center">
            <input
              type="text"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder="Message CostDeck AI..."
              className="w-full pl-4 pr-12 py-3.5 bg-slate-50 hover:bg-slate-100/80 focus:bg-white border border-slate-200 rounded-xl text-sm focus:outline-none focus:ring-2 focus:ring-emerald-500/20 focus:border-emerald-500 transition-all font-medium text-slate-800 placeholder-slate-400 shadow-sm"
            />
            <button
              type="submit"
              disabled={!input.trim() || isLoading}
              className={`absolute right-2.5 p-2 rounded-lg transition-all ${input.trim() && !isLoading ? 'bg-emerald-500 text-white hover:bg-emerald-600 shadow-sm shadow-emerald-500/20' : 'text-slate-300 bg-transparent'
                }`}
            >
              <Send size={16} />
            </button>
          </div>
        </form>
      </div>
    </>
  );
}

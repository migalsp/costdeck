import { useState } from 'react'
import { HelpCircle } from 'lucide-react'

interface InfoTooltipProps {
  content: string;
  position?: 'top' | 'bottom' | 'left' | 'right';
}

export default function InfoTooltip({ content, position = 'top' }: InfoTooltipProps) {
  const [visible, setVisible] = useState(false)

  const positions = {
    top: 'bottom-full left-1/2 -translate-x-1/2 mb-2',
    bottom: 'top-full left-1/2 -translate-x-1/2 mt-2',
    left: 'right-full top-1/2 -translate-y-1/2 mr-2',
    right: 'left-full top-1/2 -translate-y-1/2 ml-2'
  }

  const arrows = {
    top: 'top-full left-1/2 -translate-x-1/2 border-t-slate-800 border-x-transparent border-b-transparent',
    bottom: 'bottom-full left-1/2 -translate-x-1/2 border-b-slate-800 border-x-transparent border-t-transparent',
    left: 'left-full top-1/2 -translate-y-1/2 border-l-slate-800 border-y-transparent border-r-transparent',
    right: 'right-full top-1/2 -translate-y-1/2 border-r-slate-800 border-y-transparent border-l-transparent'
  }

  return (
    <div 
      className="relative inline-flex items-center"
      onMouseEnter={() => setVisible(true)}
      onMouseLeave={() => setVisible(false)}
    >
      <HelpCircle size={14} className="text-emerald-500/60 hover:text-emerald-500 transition-colors cursor-help" />
      
      {visible && (
        <div className={`absolute z-[100] whitespace-normal w-56 ${positions[position]} animate-in fade-in zoom-in duration-200`}>
          <div className="bg-slate-900/95 backdrop-blur-md text-white text-[11px] leading-relaxed font-semibold px-3 py-2.5 rounded-xl shadow-2xl border border-white/10 ring-1 ring-black/20">
            {content}
            <div className={`absolute border-4 ${arrows[position]}`} />
          </div>
        </div>
      )}
    </div>
  )
}

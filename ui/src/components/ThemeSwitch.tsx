import { Monitor, Moon, Sun } from 'lucide-react'
import { setTheme, useTheme, type Theme } from '../lib/theme'

const options: { id: Theme; label: string; icon: typeof Sun }[] = [
  { id: 'system', label: 'Same as the system', icon: Monitor },
  { id: 'light', label: 'Light', icon: Sun },
  { id: 'dark', label: 'Dark', icon: Moon },
]

// ThemeSwitch sits in the sidebar, which is dark in both themes.
export default function ThemeSwitch() {
  const theme = useTheme()
  return (
    <div className="flex items-center justify-between gap-3 px-1">
      <span className="text-[10px] font-semibold uppercase tracking-widest text-slate-500">Theme</span>
      <div role="radiogroup" aria-label="Theme" className="flex gap-0.5 rounded-lg bg-white/5 p-0.5">
        {options.map(({ id, label, icon: Icon }) => (
          <button key={id} type="button" role="radio" aria-checked={theme === id} aria-label={label} title={label}
            onClick={() => setTheme(id)}
            className={`p-1.5 rounded-md transition-colors ${theme === id ? 'bg-white/15 text-white' : 'text-slate-500 hover:text-white'}`}>
            <Icon size={14} />
          </button>
        ))}
      </div>
    </div>
  )
}

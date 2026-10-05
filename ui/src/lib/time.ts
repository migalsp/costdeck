// relativeTime renders a future timestamp as "in 3h 20m".
export function relativeTime(iso: string): string {
  const ms = new Date(iso).getTime() - Date.now()
  if (ms <= 0) return 'now'
  const minutes = Math.round(ms / 60000)
  if (minutes < 60) return `in ${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `in ${hours}h ${minutes % 60}m`
  return `in ${Math.floor(hours / 24)}d ${hours % 24}h`
}

// ago renders a past timestamp: "just now", "12 min ago", "3 h ago", "yesterday",
// then the date.
export function ago(iso: string, now = new Date()): string {
  const t = new Date(iso)
  const minutes = Math.round((now.getTime() - t.getTime()) / 60000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes} min ago`
  if (minutes < 24 * 60 && t.getDate() === now.getDate()) return `${Math.round(minutes / 60)} h ago`
  const days = Math.round((new Date(now.toDateString()).getTime() - new Date(t.toDateString()).getTime()) / 86400000)
  if (days === 1) return 'yesterday'
  if (days < 7) return `${days} days ago`
  return t.toLocaleDateString([], { month: 'short', day: 'numeric', year: t.getFullYear() === now.getFullYear() ? undefined : 'numeric' })
}

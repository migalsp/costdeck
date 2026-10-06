import { useEffect, useEffectEvent } from 'react'

// usePolling calls load right away and then every intervalMs (never, when intervalMs is
// null), starting over whenever key changes. load is an effect event: it always sees the
// current props and state without re-arming the timer, and the state it sets lands after
// its fetches resolve rather than synchronously inside the effect.
export function usePolling(load: () => void, intervalMs: number | null, key?: unknown) {
  const tick = useEffectEvent(load)
  useEffect(() => {
    tick()
    if (intervalMs === null) return
    const id = setInterval(() => tick(), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs, key])
}

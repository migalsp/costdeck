import type { NodeCheck } from './types'

// BUSY is the share of a node others still request above which keeping it is no surprise.
const BUSY = 0.5

// nodeVerdict says in a line why a recorded node is gone or still there.
export function nodeVerdict(n: NodeCheck, settled: boolean): { tone: 'success' | 'warning' | 'neutral'; text: string } {
  if (n.gone) return { tone: 'success', text: 'removed' }
  if (n.blockers.length > 0) {
    const pods = [...new Set(n.blockers.map(b => b.pod).filter(Boolean))]
    return { tone: 'warning', text: `kept by ${n.blockers.length === 1 ? 'one blocker' : `${n.blockers.length} blockers`}${pods.length ? ` on ${pods.join(', ')}` : ''}` }
  }
  if (n.requestedShare >= BUSY) {
    const top = n.tenants.slice(0, 3).map(t => t.namespace).join(', ')
    return { tone: 'neutral', text: `still needed: ${Math.round(n.requestedShare * 100)}% requested by ${top}` }
  }
  if (!settled) return { tone: 'neutral', text: 'waiting for the autoscaler' }
  return { tone: 'warning', text: `not removed although only ${Math.round(n.requestedShare * 100)}% is requested: does Cluster Autoscaler or Karpenter manage its pool?` }
}

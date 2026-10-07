const SUFFIX: Record<string, number> = {
  n: 1e-9, u: 1e-6, m: 1e-3, '': 1, k: 1e3, M: 1e6, G: 1e9, T: 1e12, P: 1e15, E: 1e18,
  Ki: 2 ** 10, Mi: 2 ** 20, Gi: 2 ** 30, Ti: 2 ** 40, Pi: 2 ** 50, Ei: 2 ** 60,
}

// quantity reads a Kubernetes quantity such as "250m", "1.5Gi", "500M" or "1e3" as a plain
// number of base units (cores, bytes). Anything it cannot read counts as 0.
export function quantity(v?: string): number {
  const m = /^([+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)([a-zA-Z]*)$/.exec((v || '').trim())
  const factor = m ? SUFFIX[m[2]] : undefined
  return m && factor !== undefined ? parseFloat(m[1]) * factor : 0
}

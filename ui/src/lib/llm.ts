import type { LLMModelPrice } from './types'

const DECIMAL = /^[0-9]+(\.[0-9]+)?$/

// llmModelProblem mirrors validateLLMModels in internal/api, so Settings says what saving
// would refuse.
export function llmModelProblem(models: LLMModelPrice[]): string | null {
  const seen = new Set<string>()
  for (const m of models) {
    const name = m.model.trim()
    if (!name || name.length > 200 || /\[[^\]]*$/.test(name)) return `model "${name}" must be a model name or a glob such as gpt-4o*`
    if (seen.has(name)) return `model "${name}" is priced twice`
    seen.add(name)
    if (!DECIMAL.test(m.inputPer1M.trim()) || !DECIMAL.test(m.outputPer1M.trim())) return `model "${name}": prices per million tokens must be plain decimal numbers such as 2.50`
  }
  return models.length > 100 ? 'at most 100 model prices' : null
}

import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import AICostsPage from './AICostsPage'
import SettingsPage from './SettingsPage'
import { tokens, type AICosts } from '../lib/finops'
import { llmModelProblem } from '../lib/llm'

const costs = (c: Partial<AICosts> = {}): AICosts => ({
  available: true, currency: 'USD', windowDays: 7,
  api: {
    monthly: 1543.2, sources: ['litellm', 'opentelemetry'], unpriced: ['mystery-model'],
    teams: [
      { team: 'payments', source: 'opentelemetry', monthly: 1000, inputTokens: 15e6, outputTokens: 2e6, namespaces: ['checkout', 'shop'] },
      { team: 'unassigned', source: 'litellm', monthly: 543.2, inputTokens: 4e5, outputTokens: 1200 },
    ],
    models: [{ model: 'claude-sonnet-4-5', monthly: 1000, inputTokens: 15e6, outputTokens: 2e6, priced: true }, { model: 'mystery-model', monthly: 0, inputTokens: 1e5, outputTokens: 0, priced: false }],
  },
  selfHosted: [{ namespace: 'llm', model: 'llama-3-8b', tokensPerHour: 24000, monthlyCost: 613.2, costPer1M: 35.28 }],
  ...c,
})

describe('AI costs', () => {
  it('shows LLM spend by team and model, unpriced models, and the cost per million tokens', async () => {
    mockFetch(on('GET', '/api/finops/ai', () => json(costs())))
    render(<AICostsPage />)
    expect(await screen.findByText('$1,543')).toBeTruthy()
    expect(screen.getByText('From LiteLLM and OpenTelemetry, last 7 days')).toBeTruthy()
    const payments = screen.getByText('payments').closest('tr')!
    expect(within(payments).getByText('checkout, shop')).toBeTruthy()
    expect(within(payments).getByText('15.0M')).toBeTruthy()
    expect(screen.getByText('No team')).toBeTruthy()
    expect(screen.getByTestId('ai-unpriced').textContent).toContain('mystery-model has no price, so its tokens count for nothing.')
    const llama = screen.getByText('llama-3-8b').closest('tr')!
    expect(within(llama).getByText('$35')).toBeTruthy()
    expect(within(llama).getByText('24k')).toBeTruthy()
  })

  it('says where the numbers come from when VictoriaMetrics is missing or has nothing', async () => {
    mockFetch(on('GET', '/api/finops/ai', () => json(costs({ available: false, api: { monthly: 0, sources: [], teams: [], models: [], unpriced: [] }, selfHosted: [] }))))
    const { unmount } = render(<AICostsPage />)
    expect((await screen.findByTestId('ai-unavailable')).textContent).toContain('Token counts come from VictoriaMetrics, which is not connected.')
    unmount()
    mockFetch(on('GET', '/api/finops/ai', () => json(costs({ api: { monthly: 0, sources: [], teams: [], models: [], unpriced: [] }, selfHosted: [] }))))
    render(<AICostsPage />)
    expect((await screen.findByTestId('ai-empty')).textContent).toContain('No LLM traffic in the last 7 days')
  })

  it('shortens token counts', () => {
    expect([tokens(950), tokens(12_400), tokens(3_250_000), tokens(7.1e9)]).toEqual(['950', '12k', '3.3M', '7.1B'])
  })

  it('checks model prices the way the operator does', () => {
    expect(llmModelProblem([{ model: 'gpt-4o', inputPer1M: '2.50', outputPer1M: '10' }, { model: 'claude-*', inputPer1M: '1', outputPer1M: '5' }])).toBeNull()
    expect(llmModelProblem([{ model: ' ', inputPer1M: '1', outputPer1M: '1' }])).toContain('must be a model name or a glob')
    expect(llmModelProblem([{ model: 'gpt-[', inputPer1M: '1', outputPer1M: '1' }])).toContain('must be a model name or a glob')
    expect(llmModelProblem([{ model: 'a', inputPer1M: '1', outputPer1M: '1' }, { model: 'a', inputPer1M: '2', outputPer1M: '2' }])).toBe('model "a" is priced twice')
    expect(llmModelProblem([{ model: 'a', inputPer1M: 'cheap', outputPer1M: '1' }])).toContain('plain decimal numbers')
  })

  it('saves model prices with the other prices, and refuses a bad one', async () => {
    window.history.replaceState(null, '', '/settings/pricing')
    const settings = { providers: {}, integrations: {}, auth: { disableLocalLogin: false }, calendars: [],
      pricing: { llmModels: [{ model: 'gpt-4o', inputPer1M: '2.50', outputPer1M: '10' }], effective: { cpuCoreHour: 0.033, memoryGiBHour: 0.00375, currency: 'USD', basis: 'x' } } }
    const fetch = mockFetch(on('GET', '/api/settings', () => json(settings)), on('PUT', '/api/settings', () => json(settings)))
    render(<SettingsPage section="pricing" />)
    const user = userEvent.setup()
    expect(await screen.findByDisplayValue('gpt-4o')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Add a model price' }))
    expect(screen.getByText('Unsaved changes')).toBeTruthy()
    const models = screen.getAllByLabelText('Model')
    await user.type(models[1], 'claude-sonnet-*')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText(/Not saved: model "claude-sonnet-\*": prices per million tokens/)).toBeTruthy()
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
    await user.type(screen.getAllByLabelText('Input price per million tokens')[1], '3')
    await user.type(screen.getAllByLabelText('Output price per million tokens')[1], '15')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const put = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')!
    expect(JSON.parse(put[1]!.body as string).pricing.llmModels).toEqual([
      { model: 'gpt-4o', inputPer1M: '2.50', outputPer1M: '10' }, { model: 'claude-sonnet-*', inputPer1M: '3', outputPer1M: '15' },
    ])
  })
})

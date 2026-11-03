import { describe, expect, it } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import AIChatWidget from './AIChatModal'
import { AuthContext, type User } from '../lib/auth'

const operator: User = { sub: 'user:ops', name: 'ops', role: 'operator', provider: 'local' }
const viewer: User = { ...operator, role: 'viewer' }
const aiOn = on('GET', '/api/settings', () => json({ integrations: { ai: { enabled: true } } }))

const sse = (...events: object[]) =>
  new Response(events.map(e => `data: ${JSON.stringify(e)}\n\n`).join(''), { headers: { 'Content-Type': 'text/event-stream' } })

const chatBodies = (fetch: ReturnType<typeof mockFetch>) =>
  fetch.mock.calls.filter(([url]) => url === '/api/ai/chat').map(([, init]) => JSON.parse(init!.body as string))
const toolCalls = (fetch: ReturnType<typeof mockFetch>) => fetch.mock.calls.filter(([url]) => String(url).startsWith('/api/ai/tools/'))

async function openChat(user: User) {
  render(<AuthContext.Provider value={user}><AIChatWidget /></AuthContext.Provider>)
  const u = userEvent.setup()
  await u.click(await screen.findByRole('button', { name: 'Open CostDeck AI' }))
  const ask = async (text: string) => {
    await u.type(screen.getByPlaceholderText('Message CostDeck AI...'), text)
    await u.click(screen.getByRole('button', { name: 'Send' }))
  }
  return { u, ask }
}

const scaleDown = { type: 'confirm', tool: 'scale_namespace', args: { namespace: 'shop-dev', action: 'down', duration: '4h' } }

describe('AI chat', () => {
  it('stays hidden while the assistant is turned off', async () => {
    const fetch = mockFetch(on('GET', '/api/settings', () => json({ integrations: { ai: { enabled: false } } })))
    render(<AuthContext.Provider value={operator}><AIChatWidget /></AuthContext.Provider>)
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/settings'))
    await act(async () => {})
    expect(screen.queryByRole('button', { name: 'Open CostDeck AI' })).toBeNull()
  })

  it('tells viewers it is read-only and operators that they confirm actions', async () => {
    mockFetch(aiOn)
    const { unmount } = render(<AuthContext.Provider value={viewer}><AIChatWidget /></AuthContext.Provider>)
    expect(await screen.findByText('Read-only assistant')).toBeTruthy()
    unmount()
    render(<AuthContext.Provider value={operator}><AIChatWidget /></AuthContext.Provider>)
    expect(await screen.findByText('Answers questions · proposes actions you confirm')).toBeTruthy()
  })

  it('streams the answer with the tools it used and sends the conversation so far', async () => {
    let turn = 0
    const fetch = mockFetch(aiOn, on('POST', '/api/ai/chat', () => {
      turn++
      return turn === 1
        ? sse({ type: 'tool_call', tool: 'get_namespace_status' }, { type: 'tool_result', tool: 'get_namespace_status', text: '{}' },
          { type: 'text', text: 'shop-dev is ' }, { type: 'text', text: '**kept up** manually.' }, { type: 'done' })
        : sse({ type: 'text', text: 'draft' }, { type: 'reset' }, { type: 'text', text: 'Until 18:00.' }, { type: 'done' })
    }))
    const { ask } = await openChat(viewer)
    await ask('Why is shop-dev up?')
    expect((await screen.findByText('kept up')).tagName).toBe('STRONG')
    expect(screen.getByText('Inspecting a namespace')).toBeTruthy()
    expect((screen.getByPlaceholderText('Message CostDeck AI...') as HTMLInputElement).value).toBe('')

    await ask('Until when?')
    expect(await screen.findByText('Until 18:00.')).toBeTruthy()
    expect(screen.queryByText(/draft/)).toBeNull()
    expect(chatBodies(fetch)).toEqual([
      { messages: [{ role: 'user', content: 'Why is shop-dev up?' }] },
      { messages: [
        { role: 'user', content: 'Why is shop-dev up?' },
        { role: 'assistant', content: 'shop-dev is **kept up** manually.' },
        { role: 'user', content: 'Until when?' },
      ] },
    ])
  })

  it('runs a proposed action only after it is confirmed, and tells the next turn it happened', async () => {
    let turn = 0
    const fetch = mockFetch(
      aiOn,
      on('POST', '/api/ai/chat', () => (++turn === 1 ? sse({ type: 'text', text: 'I can scale it down.' }, scaleDown, { type: 'done' }) : sse({ type: 'done' }))),
      on('POST', '/api/ai/tools/scale_namespace', () => json({ result: 'Scaled shop-dev down for 4h' })),
    )
    const { u, ask } = await openChat(operator)
    await ask('Scale shop-dev down for 4 hours')
    expect(await screen.findByText('Force shop-dev down for 4h')).toBeTruthy()
    expect(toolCalls(fetch)).toHaveLength(0)

    await u.click(screen.getByRole('button', { name: 'Confirm' }))
    expect((await screen.findAllByText('Scaled shop-dev down for 4h')).length).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull()
    expect(toolCalls(fetch)).toHaveLength(1)
    expect(toolCalls(fetch)[0][1]!.method).toBe('POST')
    expect(JSON.parse(toolCalls(fetch)[0][1]!.body as string)).toEqual({ args: { namespace: 'shop-dev', action: 'down', duration: '4h' } })

    await ask('Thanks')
    await waitFor(() => expect(chatBodies(fetch)).toHaveLength(2))
    const history = chatBodies(fetch)[1].messages
    expect(history[1].role).toBe('assistant')
    expect(history[1].content).toMatch(/^I can scale it down\.\n\n.*Scaled shop-dev down for 4h$/s)
  })

  it('changes nothing when a proposed action is dismissed', async () => {
    const fetch = mockFetch(aiOn, on('POST', '/api/ai/chat', () => sse(
      { type: 'confirm', tool: 'scale_group', args: { name: 'team-a', action: 'resume' } },
      { type: 'confirm', tool: 'revert_optimization', args: { namespace: 'shop-dev' } },
      { type: 'done' },
    )))
    const { u, ask } = await openChat(operator)
    await ask('Hand team-a back')
    expect(await screen.findByText('Hand team-a back to its schedule')).toBeTruthy()
    expect(screen.getByText('Restore the original requests and limits in shop-dev')).toBeTruthy()
    await u.click(screen.getAllByRole('button', { name: 'Dismiss' })[0])
    expect(screen.getByText(/nothing was changed/)).toBeTruthy()
    expect(screen.getAllByRole('button', { name: 'Confirm' })).toHaveLength(1)
    expect(toolCalls(fetch)).toHaveLength(0)
  })

  it('shows why a confirmed action failed on its card', async () => {
    mockFetch(
      aiOn,
      on('POST', '/api/ai/chat', () => sse(scaleDown, { type: 'done' })),
      on('POST', '/api/ai/tools/scale_namespace', () => json({ error: 'This action needs the operator role; you are signed in as viewer.' }, 403)),
    )
    const { u, ask } = await openChat(operator)
    await ask('Scale it down')
    await u.click(await screen.findByRole('button', { name: 'Confirm' }))
    expect(await screen.findByText('This action needs the operator role; you are signed in as viewer.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull()
  })

  it('shows a refused request and an error reported mid-answer', async () => {
    let turn = 0
    mockFetch(aiOn, on('POST', '/api/ai/chat', () => (++turn === 1
      ? json({ error: 'AI integration is disabled' }, 400)
      : sse({ type: 'text', text: 'Partial' }, { type: 'error', text: 'The model declined to answer this request.' }, { type: 'done' }))))
    const { ask } = await openChat(viewer)
    await ask('Hello')
    expect(await screen.findByText('AI integration is disabled')).toBeTruthy()
    await ask('Again')
    expect(await screen.findByText('The model declined to answer this request.')).toBeTruthy()
    expect(screen.getByText('Partial')).toBeTruthy()
  })

  it('stops a running answer without reporting an error', async () => {
    mockFetch(aiOn, (url, init) => (url === '/api/ai/chat'
      ? new Promise<Response>((_, reject) => init!.signal!.addEventListener('abort', () => reject(new DOMException('The operation was aborted.', 'AbortError'))))
      : undefined))
    const { u, ask } = await openChat(viewer)
    await ask('Something slow')
    expect(await screen.findByText('Thinking…')).toBeTruthy()
    await u.click(screen.getByRole('button', { name: 'Stop' }))
    expect(await screen.findByRole('button', { name: 'Send' })).toBeTruthy()
    expect(screen.queryByText('Thinking…')).toBeNull()
    expect(screen.queryByText(/abort/i)).toBeNull()
  })

  it('puts together events split across network chunks and skips keep-alives', async () => {
    const encoder = new TextEncoder()
    const chunks = [
      ': keep-alive\n\n',
      'data: {"type":"text","te',
      'xt":"Hello "}\n',
      '\ndata: {"type":"text","text":"world"}\n\ndata: not json\n\n',
      'data: {"type":"done"}\n\n',
    ]
    mockFetch(aiOn, on('POST', '/api/ai/chat', () => new Response(new ReadableStream({
      start(controller) {
        for (const c of chunks) controller.enqueue(encoder.encode(c))
        controller.close()
      },
    }), { headers: { 'Content-Type': 'text/event-stream' } })))
    const { ask } = await openChat(viewer)
    await ask('Hi')
    expect(await screen.findByText('Hello world')).toBeTruthy()
    expect(screen.queryByText(/not json|keep-alive/)).toBeNull()
  })

  it('fills in a suggested question and starts a new chat', async () => {
    mockFetch(aiOn, on('POST', '/api/ai/chat', () => sse({ type: 'text', text: 'Nothing tonight.' }, { type: 'done' })))
    const { u } = await openChat(viewer)
    await u.click(screen.getByRole('button', { name: 'What scales down tonight?' }))
    expect((screen.getByPlaceholderText('Message CostDeck AI...') as HTMLInputElement).value).toBe('What scales down tonight?')
    await u.click(screen.getByRole('button', { name: 'Send' }))
    expect(await screen.findByText('Nothing tonight.')).toBeTruthy()
    await u.click(screen.getByRole('button', { name: 'New chat' }))
    expect(screen.queryByText('Nothing tonight.')).toBeNull()
    expect(screen.getByText('Ask about costs, waste, or schedules.')).toBeTruthy()
  })
})

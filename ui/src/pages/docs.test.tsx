import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import Documentation from './Documentation'
import ApiExplorer from './docs/ApiExplorer'
import { guides } from './docs/content'
import { navigate } from '../lib/router'

beforeEach(() => { window.history.replaceState(null, '', '/docs') })

const spec = {
  info: { version: '1.6.2' },
  tags: [{ name: 'Schedules', description: 'Scaling groups and configs.' }, { name: 'Access', description: 'Tokens and users.' }],
  paths: {
    '/api/tokens': {
      get: {
        summary: 'List API tokens', tags: ['Access'], 'x-role': 'admin',
        responses: { '200': { description: 'ok', content: { 'application/json': { schema: { type: 'array', items: { $ref: '#/components/schemas/Token' } } } } } },
      },
      post: {
        summary: 'Create an API token', tags: ['Access'], 'x-role': 'admin',
        requestBody: { content: { 'application/json': { schema: { type: 'object', properties: {
          name: { type: 'string', example: "bob's ci" }, role: { type: 'string', enum: ['viewer', 'operator', 'admin'] }, expiresInDays: { type: 'integer', default: 90 },
        } } } } },
      },
    },
    '/api/scaling/groups/{name}/manual': {
      parameters: [{ $ref: '#/components/parameters/Name' }],
      post: {
        summary: 'Start, stop or resume a group', tags: ['Schedules'], 'x-role': 'operator',
        requestBody: { content: { 'application/json': { examples: {
          stop: { summary: 'Scale down for 4 hours', value: { active: false, until: '4h' } },
          resume: { summary: 'Follow the schedule', value: { active: null } },
        } } } },
      },
    },
    '/api/version': { get: { summary: 'Version of the operator', deprecated: true } },
    '/api/scaling/groups': {
      get: { summary: 'List schedules', tags: ['Schedules'], 'x-role': 'viewer', parameters: [{ name: 'dryRun', in: 'query', description: 'Only dry runs.', schema: { type: 'string', enum: ['true', 'false'] } }] },
    },
  },
  components: {
    schemas: { Token: { type: 'object', properties: { name: { type: 'string' }, createdAt: { type: 'string', format: 'date-time' }, role: { type: 'string', enum: ['viewer'] } } } },
    parameters: { Name: { name: 'name', in: 'path', required: true, description: 'The schedule.' } },
  },
}

const endpoint = (method: string, path: string) =>
  screen.getByRole('button', { name: new RegExp(`^${method}${path.replace(/[{}/]/g, c => `\\${c}`)}(?![/{])`) })
const code = (scope: HTMLElement, label: string) => within(scope).getByText(label, { selector: 'span' }).closest('.theme-keep')!.querySelector('pre')!.textContent
const card = (method: string, path: string) => endpoint(method, path).parentElement as HTMLElement

describe('Documentation', () => {
  it('falls back to the first guide and steps to the next one', async () => {
    render(<Documentation guide="no-such-guide" />)
    expect(screen.getByRole('heading', { level: 1, name: guides[0].title })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /^←/ })).toBeNull()
    await userEvent.setup().click(screen.getByRole('button', { name: `${guides[1].title} →` }))
    expect(window.location.pathname).toBe(`/docs/${guides[1].id}`)
  })

  it('leads from the last guide to the REST API and back', async () => {
    mockFetch(on('GET', '/api/openapi.json', () => json(spec)))
    const last = guides[guides.length - 1]
    const { unmount } = render(<Documentation guide={last.id} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'REST API →' }))
    expect(window.location.pathname).toBe('/docs/api')
    unmount()
    render(<Documentation guide="api" />)
    expect(await screen.findByRole('heading', { level: 1, name: 'REST API' })).toBeTruthy()
    expect(await screen.findByText('Version 1.6.2')).toBeTruthy()
  })

  it('scrolls to the topic named in the address', () => {
    const scrolled = vi.spyOn(Element.prototype, 'scrollIntoView')
    act(() => navigate('/docs/schedules#exceptions'))
    render(<Documentation guide="schedules" />)
    expect(scrolled.mock.contexts.map(el => (el as Element).id)).toContain('doc-schedules-exceptions')
  })

  it('links every topic of a guide to its own address', async () => {
    render(<Documentation guide="schedules" />)
    const link = screen.getByRole('link', { name: 'Holidays and other exceptions' })
    expect(link.getAttribute('href')).toBe('/docs/schedules#exceptions')
    await userEvent.setup().click(link)
    expect(window.location.pathname + window.location.hash).toBe('/docs/schedules#exceptions')
  })

  it('searches every guide and opens the topic that matched', async () => {
    render(<Documentation guide="start" />)
    const user = userEvent.setup()
    const search = screen.getByPlaceholderText('Search the guides')
    await user.type(search, 's')
    expect(screen.getByText('Type at least two letters.')).toBeTruthy()
    await user.type(search, 'leep page')
    expect(screen.getByRole('heading', { level: 1, name: 'Search' })).toBeTruthy()
    expect(screen.getByText(/^\d+ topics? mention “sleep page”\.$/)).toBeTruthy()
    const hit = screen.getByRole('button', { name: /A sleep page while down/ })
    expect(hit.textContent).toContain('Schedules')
    await user.click(hit)
    expect(window.location.pathname + window.location.hash).toBe('/docs/schedules#sleep-page')
    expect((search as HTMLInputElement).value).toBe('')
  })

  it('says when nothing matches', async () => {
    render(<Documentation guide="start" />)
    await userEvent.setup().type(screen.getByPlaceholderText('Search the guides'), 'qqzzxx')
    expect(screen.getByText('0 topics mention “qqzzxx”.')).toBeTruthy()
  })
})

describe('REST API reference', () => {
  it('groups the endpoints by tag in the order of the document, with the role each needs', async () => {
    mockFetch(on('GET', '/api/openapi.json', () => json(spec)))
    render(<ApiExplorer />)
    expect(screen.getByText('Loading the API description…')).toBeTruthy()
    expect(await screen.findByText('Version 1.6.2')).toBeTruthy()
    expect(screen.getAllByRole('heading', { level: 2 }).map(h => h.textContent)).toEqual(['Schedules', 'Access', 'Other'])
    expect(screen.getByText('Scaling groups and configs.')).toBeTruthy()
    const schedules = screen.getByRole('heading', { level: 2, name: 'Schedules' }).parentElement as HTMLElement
    expect(within(schedules).getAllByRole('button').map(b => b.querySelector('code')?.textContent)).toEqual(['/api/scaling/groups', '/api/scaling/groups/{name}/manual'])
    expect(within(card('GET', '/api/scaling/groups')).getByText('viewer')).toBeTruthy()
    expect(within(card('POST', '/api/scaling/groups/{name}/manual')).getByText('operator')).toBeTruthy()
    expect(within(card('POST', '/api/tokens')).getByText('admin')).toBeTruthy()
    const version = card('GET', '/api/version')
    expect(within(version).getByText('Public')).toBeTruthy()
    expect(within(version).getByText('Deprecated')).toBeTruthy()
  })

  it('prints a curl line that runs as shown, with a body built from the schema', async () => {
    mockFetch(on('GET', '/api/openapi.json', () => json(spec)))
    render(<ApiExplorer />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /^POST\/api\/tokens/ }))
    const post = card('POST', '/api/tokens')
    expect(code(post, 'curl')).toBe([
      'curl -X POST "$COSTDECK/api/tokens" \\',
      '  -H "Authorization: Bearer $TOKEN" \\',
      '  -H "Content-Type: application/json" \\',
      `  -d '{"name":"bob'\\''s ci","role":"viewer","expiresInDays":90}'`,
    ].join('\n'))
    expect(JSON.parse(code(post, 'Request body')!)).toEqual({ name: "bob's ci", role: 'viewer', expiresInDays: 90 })

    await user.click(screen.getByRole('button', { name: /^GET\/api\/tokens/ }))
    const get = card('GET', '/api/tokens')
    expect(within(post).queryByText('curl')).toBeNull()
    expect(code(get, 'curl')).toBe('curl "$COSTDECK/api/tokens" \\\n  -H "Authorization: Bearer $TOKEN"')
    expect(JSON.parse(code(get, 'Response')!)).toEqual([{ name: 'string', createdAt: '2026-10-05T18:00:00Z', role: 'viewer' }])
  })

  it('fills in path parameters and switches between the named examples', async () => {
    mockFetch(on('GET', '/api/openapi.json', () => json(spec)))
    render(<ApiExplorer />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /^POST\/api\/scaling\/groups\/\{name\}\/manual/ }))
    const manual = card('POST', '/api/scaling/groups/{name}/manual')
    const param = within(manual).getByRole('row', { name: /name/ })
    expect(param.textContent).toBe('name*pathThe schedule.')
    expect(code(manual, 'curl')).toContain(`"$COSTDECK/api/scaling/groups/dev/manual"`)
    expect(code(manual, 'curl')).toContain(`-d '{"active":false,"until":"4h"}'`)
    await user.click(within(manual).getByRole('button', { name: 'Follow the schedule' }))
    expect(code(manual, 'curl')).toContain(`-d '{"active":null}'`)
    expect(JSON.parse(code(manual, 'Body')!)).toEqual({ active: null })

    await user.click(screen.getByRole('button', { name: /^GET\/api\/scaling\/groups(?!\/)/ }))
    const list = card('GET', '/api/scaling/groups')
    expect(within(list).getByRole('row', { name: /dryRun/ }).textContent).toBe('dryRunqueryOnly dry runs. One of true, false.')
  })

  it('filters the endpoints and says when none matches', async () => {
    mockFetch(on('GET', '/api/openapi.json', () => json(spec)))
    render(<ApiExplorer />)
    const user = userEvent.setup()
    const filter = await screen.findByPlaceholderText(/Filter endpoints/)
    await screen.findByText('Version 1.6.2')
    await user.type(filter, 'TOKENS')
    expect(screen.getAllByRole('heading', { level: 2 }).map(h => h.textContent)).toEqual(['Access'])
    await user.clear(filter)
    await user.type(filter, 'delete')
    expect(screen.getByText('No endpoint matches “delete”.')).toBeTruthy()
  })

  it('says when the API description cannot be loaded', async () => {
    mockFetch(on('GET', '/api/openapi.json', () => new Response('nope', { status: 500 })))
    render(<ApiExplorer />)
    expect(await screen.findByText('Could not load /api/openapi.json (HTTP 500).')).toBeTruthy()
    expect(screen.queryByText('Loading the API description…')).toBeNull()
  })
})

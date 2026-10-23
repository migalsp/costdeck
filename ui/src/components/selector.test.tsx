import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import ScheduleWizard from './ScheduleWizard'
import type { ScalingGroup } from '../lib/types'

const other: ScalingGroup = { metadata: { name: 'shop' }, spec: { category: 'x', namespaces: ['pr-2'] } } as ScalingGroup

describe('ScheduleWizard label selector', () => {
  it('previews what a selector matches and saves a group defined by labels only', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('GET', '/api/namespace-selector', url => new URL(url, 'http://x').searchParams.get('selector') === 'env=preview'
        ? json({ selector: { matchLabels: { env: 'preview' } }, namespaces: ['pr-1', 'pr-2'] })
        : json({ error: 'unable to parse requirement' }, 400)),
      on('POST', '/api/scaling/groups', (_, init) => json(JSON.parse(init!.body as string), 201)),
    )
    const onSaved = vi.fn()
    render(<ScheduleWizard kind="group" namespaces={['pr-1', 'pr-2', 'prod']} groups={[other]} onClose={() => {}} onSaved={onSaved} />)
    const user = userEvent.setup()
    // A fresh form does not nag; it only refuses to save.
    expect(screen.queryByText('Pick at least one namespace, or match them by label.')).toBeNull()
    expect((screen.getByRole('button', { name: 'Create schedule' }) as HTMLButtonElement).disabled).toBe(true)

    await user.type(screen.getByLabelText('Label selector'), 'env in (')
    expect(await screen.findByText('unable to parse requirement')).toBeTruthy()
    expect(screen.getByText('Label selector: unable to parse requirement')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Create schedule' }) as HTMLButtonElement).disabled).toBe(true)

    await user.clear(screen.getByLabelText('Label selector'))
    await user.type(screen.getByLabelText('Label selector'), 'env=preview')
    expect((await screen.findByTestId('selector-matches')).textContent).toBe('Matches now: pr-1, pr-2 (in shop)')
    await user.click(screen.getByRole('button', { name: 'Create schedule' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    const post = fetch.mock.calls.find(([, init]) => init?.method === 'POST')!
    const body = JSON.parse(post[1]!.body as string)
    expect(body.metadata.name).toBe('env-preview')
    expect(body.spec.namespaces).toEqual([])
    expect(body.spec.namespaceSelector).toEqual({ matchLabels: { env: 'preview' } })
  })

  it('shows the selector of an existing group and drops it when cleared', async () => {
    const existing = { metadata: { name: 'previews' }, spec: { category: 'x', namespaces: ['prod'], namespaceSelector: { matchLabels: { env: 'preview' } } } } as ScalingGroup
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('GET', '/api/namespace-selector', () => json({ selector: { matchLabels: { env: 'preview' } }, namespaces: [] })),
      on('PUT', '/api/scaling/groups/previews', (_, init) => json(JSON.parse(init!.body as string))),
    )
    render(<ScheduleWizard kind="group" existing={existing} namespaces={['prod']} groups={[existing]} onClose={() => {}} onSaved={() => {}} />)
    const user = userEvent.setup()
    expect((screen.getByLabelText('Label selector') as HTMLInputElement).value).toBe('env=preview')
    expect(await screen.findByText('Matches no namespace yet.')).toBeTruthy()
    await user.clear(screen.getByLabelText('Label selector'))
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(true))
    const body = JSON.parse(fetch.mock.calls.find(([, init]) => init?.method === 'PUT')![1]!.body as string)
    expect(body.spec.namespaceSelector).toBeUndefined()
    expect(body.spec.namespaces).toEqual(['prod'])
  })
})

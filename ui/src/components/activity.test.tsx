import { describe, expect, it } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import ScheduleActivity from './ScheduleActivity'
import type { ScalingGroup } from '../lib/types'

const teamA = (status: Partial<NonNullable<ScalingGroup['status']>> = {}, spec: Partial<ScalingGroup['spec']> = {}): ScalingGroup => ({
  metadata: { name: 'team-a' },
  spec: { category: 'dev', namespaces: ['db', 'api', 'web'], sequence: ['db', 'api'], featureFlags: { skipOnTimeout: true, timeoutMinutes: 3 }, ...spec },
  status: { phase: 'ScalingUp', lastAction: '', managedCount: 3, desiredState: 'Up', readyNamespaces: ['db'], namespacesReady: 1, namespacesTotal: 3, ...status },
})
const shared: ScalingGroup = { metadata: { name: 'shared' }, spec: { category: 'dev', namespaces: ['mq'] }, status: { phase: 'ScaledUp', lastAction: '', managedCount: 1 } }
const event = (name: string, type: string, reason: string, message: string, at: string, extra: object = {}) =>
  ({ metadata: { name, creationTimestamp: at }, type, reason, message, ...extra })

const answer = (group: ScalingGroup, extra: Parameters<typeof mockFetch> = []) => mockFetch(
  on('GET', '/api/scaling/groups', () => json([group, shared])),
  ...extra,
)
const node = (label: RegExp) => screen.getByRole('button', { name: label })
const column = (title: string) => screen.getByText(title).parentElement!.parentElement as HTMLElement
const urls = (fetch: ReturnType<typeof mockFetch>) => fetch.mock.calls.map(([url]) => url)

describe('Schedule activity', () => {
  it('draws the stages while scaling up and lists what needs attention', async () => {
    const group = teamA(
      { requiredBy: ['frontend'], conditions: [{ type: 'Ready', status: 'False', reason: 'Scaling', message: 'Stage 2 of 3 is starting' }, { type: 'ManualOverride', status: 'False', reason: 'x', message: 'ignored' }] },
      { dependsOn: ['shared', 'ghost'] },
    )
    const fetch = answer(group, [
      on('GET', '/api/scaling/groups/team-a/events', () => json([
        event('e1', 'Normal', 'ScaledUp', 'db is up', '2026-10-31T10:00:00Z'),
        event('e2', 'Warning', 'BackOff', 'api-1 keeps restarting', '2026-10-31T10:05:00Z', { series: { count: 3, lastObservedTime: '2026-10-31T10:06:00Z' } }),
      ])),
      on('GET', '/api/namespaces/api/pods', () => json([
        { name: 'api-1', status: 'Running', ready: false, reason: 'CrashLoopBackOff', restarts: 4 },
        { name: 'api-2', status: 'Running', ready: true, restarts: 0 },
      ])),
    ])
    render(<ScheduleActivity group={group} />)
    expect(screen.getByText('Scaling up: left to right, stage by stage')).toBeTruthy()
    expect(screen.getByText('1 of 3 namespaces ready')).toBeTruthy()

    expect(await screen.findByText('Depends on "ghost", which does not exist.')).toBeTruthy()
    expect(await screen.findByText('api/api-1: CrashLoopBackOff, 4 restarts')).toBeTruthy()
    const attention = screen.getByText('Needs attention').parentElement as HTMLElement
    expect(within(attention).getByText('Stage 2 of 3 is starting')).toBeTruthy()
    expect(within(attention).queryByText('ignored')).toBeNull()
    expect(within(attention).getByText(/api-1 keeps restarting/).textContent).toBe('BackOff: api-1 keeps restarting')

    expect(within(column('Stage 1')).getByText('ready')).toBeTruthy()
    expect(within(column('Stage 2')).getByText('in progress')).toBeTruthy()
    expect(within(column('Stage 2')).getByText('1 pod failing')).toBeTruthy()
    expect(within(column('Stage 3')).getByText('waiting')).toBeTruthy()
    expect(within(column('Starts after')).getByText('ScaledUp')).toBeTruthy()
    expect(within(column('Starts after')).getByText('not found')).toBeTruthy()
    expect(within(column('Kept up for')).getByText('frontend')).toBeTruthy()

    const events = screen.getByRole('heading', { name: 'Events' }).nextElementSibling as HTMLElement
    const items = within(events).getAllByRole('listitem')
    expect(items.map(li => li.textContent?.replace(/^.*?(ScaledUp|BackOff)/, '$1'))).toEqual(['BackOffapi-1 keeps restarting ×3', 'ScaledUpdb is up'])

    expect(urls(fetch)).toContain('/api/namespaces/api/pods')
    expect(urls(fetch)).not.toContain('/api/namespaces/web/pods')
    expect(urls(fetch)).not.toContain('/api/namespaces/db/pods')
  })

  it('draws only the group\'s own namespaces and cloud targets from the sequence, as the operator scales them', async () => {
    const group = teamA({ conflictingNamespaces: ['web'], readyNamespaces: [] }, { sequence: ['db stranger', 'ext:orders-db', 'web', 'api'] })
    answer(group, [on('GET', '/api/scaling/groups/team-a/events', () => json([]))])
    render(<ScheduleActivity group={group} />)
    expect(await screen.findByText('Stage 1')).toBeTruthy()
    expect(screen.queryByText('stranger')).toBeNull()
    expect(within(column('Stage 1')).getByText('db')).toBeTruthy()
    expect(within(column('Stage 2')).getByText('orders-db')).toBeTruthy()
    expect(within(column('Stage 3')).getByText('api')).toBeTruthy()
    expect(screen.queryByText('Stage 4')).toBeNull()
    expect(screen.queryByRole('button', { name: /^web/ })).toBeNull()
  })

  it('says which namespaces the timeout skipped and moves on to the next stage', async () => {
    const group = teamA({ skippedNamespaces: ['api'] })
    const fetch = answer(group)
    render(<ScheduleActivity group={group} />)
    expect(screen.getByText('api was not ready within 3 min; the next stages went ahead without it.')).toBeTruthy()
    expect(within(column('Stage 2')).getByText('skipped after timeout')).toBeTruthy()
    expect(within(column('Stage 3')).getByText('in progress')).toBeTruthy()
    expect(within(column('Stage 3')).getByText('starting')).toBeTruthy()
    await waitFor(() => expect(urls(fetch)).toContain('/api/namespaces/web/pods'))
    expect(urls(fetch)).toContain('/api/namespaces/api/pods')
  })

  it('runs the stages in reverse when scaling down', () => {
    const group = teamA({ phase: 'ScalingDown', desiredState: 'Down', readyNamespaces: ['web'], namespacesReady: 1 })
    answer(group)
    render(<ScheduleActivity group={group} />)
    expect(screen.getByText('Scaling down: right to left, stage by stage')).toBeTruthy()
    expect(screen.getByText('1 of 3 namespaces down')).toBeTruthy()
    expect(within(column('Stage 3')).getByText('down')).toBeTruthy()
    expect(within(column('Stage 2')).getByText('in progress')).toBeTruthy()
    expect(within(column('Stage 2')).getByText('stopping')).toBeTruthy()
    expect(within(column('Stage 1')).getByText('waiting')).toBeTruthy()
  })

  it('opens a namespace to show its workloads and failing pods', async () => {
    const group = teamA({ phase: 'ScaledUp', readyNamespaces: ['db', 'api', 'web'], namespacesReady: 3 })
    answer(group, [
      on('GET', '/api/namespaces/web/workloads', () => json([{ name: 'web', kind: 'Deployment', replicas: 2, readyReplicas: 1 }])),
      on('GET', '/api/namespaces/web/pods', () => json([{ name: 'web-1', status: 'Pending', ready: false, restarts: 0 }])),
    ])
    render(<ScheduleActivity group={group} />)
    expect(screen.getByText('Up and ready')).toBeTruthy()
    expect(screen.queryByText('Needs attention')).toBeNull()
    expect(screen.getByText('No events yet. They appear as soon as the schedule scales something.')).toBeTruthy()
    const user = userEvent.setup()
    await user.click(node(/^web/))
    const details = screen.getByText('Workloads').closest('.rounded-xl') as HTMLElement
    expect(await within(details).findByText('1/2')).toBeTruthy()
    expect(await within(details).findByText('web-1')).toBeTruthy()
    expect(within(details).getByText('Pending')).toBeTruthy()
    await user.click(within(details).getByRole('button', { name: 'Close' }))
    expect(screen.queryByText('Workloads')).toBeNull()
  })
})

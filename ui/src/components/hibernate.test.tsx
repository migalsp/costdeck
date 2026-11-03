import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { json, mockFetch, on } from '../test/fetch'
import HibernateEditor from './HibernateEditor'
import ScheduleWizard from './ScheduleWizard'
import SchedulePreview from './SchedulePreview'
import ScheduleDetails from './ScheduleDetails'
import WorkloadRulesDialog from './WorkloadRulesDialog'
import type { HibernateRule, ScalingGroup } from '../lib/types'

// Editor holds the rules as a parent would, and reports every change.
function Editor({ initial, onRules }: { initial: HibernateRule[]; onRules: (r: HibernateRule[]) => void }) {
  const [rules, setRules] = useState(initial)
  return <HibernateEditor rules={rules} onChange={r => { setRules(r); onRules(r) }} />
}

describe('Services to stop', () => {
  it('turns presets on and off and limits them to names', async () => {
    const onRules = vi.fn()
    render(<Editor initial={[]} onRules={onRules} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('checkbox', { name: /CloudNativePG/ }))
    expect(onRules).toHaveBeenLastCalledWith([{ preset: 'cnpg' }])
    // The comma survives while the next name is typed.
    await user.type(screen.getByRole('textbox', { name: 'CloudNativePG names' }), 'pg-*, ')
    expect((screen.getByRole('textbox', { name: 'CloudNativePG names' }) as HTMLInputElement).value).toBe('pg-*, ')
    await user.type(screen.getByRole('textbox', { name: 'CloudNativePG names' }), 'reports')
    expect(onRules).toHaveBeenLastCalledWith([{ preset: 'cnpg', names: ['pg-*', 'reports'] }])
    await user.click(screen.getByRole('checkbox', { name: /CloudNativePG/ }))
    expect(onRules).toHaveBeenLastCalledWith([])
  })

  it('describes another operator and flags a rule the operator would refuse', async () => {
    const onRules = vi.fn()
    render(<Editor initial={[]} onRules={onRules} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Another operator' }))
    expect(screen.getByText('set the apiVersion, kind and replicas field')).toBeTruthy()
    await user.type(screen.getByRole('textbox', { name: 'apiVersion' }), 'redis.example.com/v1')
    await user.type(screen.getByRole('textbox', { name: 'Kind' }), 'Redis')
    await user.type(screen.getByRole('textbox', { name: 'Replicas field' }), 'size')
    expect(screen.getByText('fields are dotted paths such as spec.replicas')).toBeTruthy()
    await user.clear(screen.getByRole('textbox', { name: 'Replicas field' }))
    await user.type(screen.getByRole('textbox', { name: 'Replicas field' }), 'spec.size')
    expect(screen.queryByText(/dotted paths/)).toBeNull()
    expect(onRules).toHaveBeenLastCalledWith([{ apiVersion: 'redis.example.com/v1', kind: 'Redis', replicasField: 'spec.size' }])
  })

  it('shows the names of the rule that moved up when one above is removed', async () => {
    render(<Editor onRules={() => {}} initial={[
      { apiVersion: 'a.io/v1', kind: 'A', replicasField: 'spec.replicas', names: ['first'] },
      { apiVersion: 'b.io/v1', kind: 'B', replicasField: 'spec.replicas', names: ['second'] },
    ]} />)
    const user = userEvent.setup()
    await user.click(screen.getAllByTitle('Remove rule')[0])
    expect((screen.getByRole('textbox', { name: 'Names' }) as HTMLInputElement).value).toBe('second')
  })

  it('saves services with a namespace\'s workload rules, and refuses a bad rule', async () => {
    mockFetch(on('GET', '/api/namespaces/shop-dev/workloads', () => json([])))
    const onSave = vi.fn()
    render(<WorkloadRulesDialog namespace="shop-dev" spec={{ targetNamespace: 'shop-dev', exclusions: ['redis'] }} onClose={() => {}} onSave={onSave} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('checkbox', { name: /Zalando Postgres/ }))
    await user.click(screen.getByRole('button', { name: 'Another operator' }))
    expect((screen.getByRole('button', { name: 'Save rules' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText('Services to stop, rule 2: set the apiVersion, kind and replicas field')).toBeTruthy()
    await user.click(screen.getByTitle('Remove rule'))
    await user.click(screen.getByRole('button', { name: 'Save rules' }))
    expect(onSave).toHaveBeenCalledWith({ targetNamespace: 'shop-dev', exclusions: ['redis'], sequence: undefined, hibernate: [{ preset: 'zalando-postgres' }] })
  })

  it('lets a namespace\'s own schedule move past a stage that is not ready in time, and keeps the choice', async () => {
    mockFetch(on('GET', '/api/namespaces/shop-dev/workloads', () => json([])))
    const onSave = vi.fn()
    const { unmount } = render(<WorkloadRulesDialog namespace="shop-dev" spec={{ targetNamespace: 'shop-dev', sequence: ['db', '*'] }} onClose={() => {}} onSave={onSave} />)
    const user = userEvent.setup()
    const minutes = screen.getByRole('spinbutton', { name: 'Minutes a stage may take' }) as HTMLInputElement
    expect(minutes.disabled).toBe(true)
    await user.click(screen.getByRole('checkbox', { name: /Start the next stage if one is not ready after/ }))
    await user.tripleClick(minutes)
    await user.keyboard('3')
    await user.click(screen.getByRole('button', { name: 'Save rules' }))
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ sequence: ['db', '*'], featureFlags: { skipOnTimeout: true, timeoutMinutes: 3 } }))
    unmount()

    render(<WorkloadRulesDialog namespace="shop-dev" spec={{ targetNamespace: 'shop-dev', featureFlags: { skipOnTimeout: true, timeoutMinutes: 3 } }} onClose={() => {}} onSave={onSave} />)
    const checkbox = screen.getByRole('checkbox', { name: /Start the next stage if one is not ready after/ }) as HTMLInputElement
    expect(checkbox.checked).toBe(true)
    expect((screen.getByRole('spinbutton', { name: 'Minutes a stage may take' }) as HTMLInputElement).value).toBe('3')
    await user.click(checkbox)
    await user.click(screen.getByRole('button', { name: 'Save rules' }))
    expect(onSave.mock.lastCall![0].featureFlags).toBeUndefined()
  })

  it('saves a group that stops services in every namespace', async () => {
    const fetch = mockFetch(
      on('GET', '/api/settings', () => json({ calendars: [] })),
      on('POST', '/api/scaling/groups', (_, init) => json(JSON.parse(init!.body as string), 201)),
    )
    const onSaved = vi.fn()
    render(<ScheduleWizard kind="group" namespaces={['shop-dev']} groups={[]} onClose={() => {}} onSaved={onSaved} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'shop-dev' }))
    await user.click(screen.getByRole('button', { name: /Advanced/ }))
    await user.click(screen.getByRole('checkbox', { name: /Strimzi/ }))
    expect(screen.getByRole('button', { name: /Advanced ·/ })).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Create schedule' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    const body = JSON.parse(fetch.mock.calls.find(([url, init]) => url === '/api/scaling/groups' && init?.method === 'POST')![1]!.body as string)
    expect(body.spec.hibernate).toEqual([{ preset: 'strimzi' }])
  })

  it('lists the services a schedule stops and leaves their workloads to the operator', async () => {
    mockFetch(on('POST', '/api/scaling/preview', () => json({
      namespaces: [{
        name: 'shop-dev', source: 'listed', cronJobs: 0,
        workloads: [
          { kind: 'Deployment', name: 'api', replicas: 2, cpu: '1', memory: '1Gi' },
          { kind: 'Deployment', name: 'db-pooler', replicas: 1, cpu: '1', memory: '1Gi', excluded: true, managedBy: 'Pooler' },
          { kind: 'Deployment', name: 'redis', replicas: 1, cpu: '1', memory: '1Gi', excluded: true },
        ],
        services: [{ kind: 'Cluster', name: 'db', running: true }, { kind: 'Cluster', name: 'archive', running: false }],
      }],
      cloudTargets: [], intervals: [], upShare: 0.3, savings: { hourly: 1, monthly: 500, currency: 'USD', basis: 'x' }, problems: [], warnings: [],
    })))
    render(<SchedulePreview kind="group" spec={{ namespaces: ['shop-dev'], hibernate: [{ preset: 'cnpg' }] }} />)
    expect((await screen.findByTestId('preview-services')).textContent).toBe('Then stops Cluster db, Cluster archive (stopped by someone else, left alone)')
    expect(screen.getByText(/kept running: redis · left to their operator: db-pooler/)).toBeTruthy()
  })

  it('shows a group\'s services on its card', () => {
    const group = {
      metadata: { name: 'shop' }, spec: { category: 'x', namespaces: ['shop-dev'], hibernate: [{ preset: 'cnpg', names: ['pg-*'] }, { apiVersion: 'redis.example.com/v1', kind: 'Redis', replicasField: 'spec.size' }] },
      status: { phase: 'ScaledUp' },
    } as unknown as ScalingGroup
    render(<ScheduleDetails target={{ kind: 'group', group }} groups={[group]} tab="overview" onTab={() => {}} onClose={() => {}} onSelectNamespace={() => {}} canOperate={false} canAdmin={false}
      onStart={() => {}} onStop={() => {}} onResume={() => {}} onEdit={() => {}} onDelete={() => {}} />)
    expect(screen.getByTestId('hibernate-rules').textContent).toContain('CloudNativePG named pg-* · Redis (redis.example.com/v1): stopped after the workloads, started before them.')
  })
})

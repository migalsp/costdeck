import { describe, expect, it } from 'vitest'
import { formatSelector, groupNamespaces } from './selector'
import type { ScalingGroup } from './types'

describe('formatSelector', () => {
  it('writes kubectl syntax, labels first and sorted', () => {
    expect(formatSelector({
      matchLabels: { team: 'shop', env: 'preview' },
      matchExpressions: [
        { key: 'tier', operator: 'In', values: ['a', 'b'] },
        { key: 'zone', operator: 'NotIn', values: ['x'] },
        { key: 'owner', operator: 'Exists' },
        { key: 'legacy', operator: 'DoesNotExist' },
      ],
    })).toBe('env=preview,team=shop,tier in (a,b),zone notin (x),owner,!legacy')
  })
  it('is empty without a selector', () => expect(formatSelector(undefined)).toBe(''))
})

describe('groupNamespaces', () => {
  const g = (spec: Partial<ScalingGroup['spec']>, selected?: string[]) =>
    ({ metadata: { name: 'g' }, spec: { category: 'x', ...spec }, status: { selectedNamespaces: selected } }) as ScalingGroup
  it('joins listed and selected namespaces, sorted, without duplicates', () => {
    expect(groupNamespaces(g({ namespaces: ['b', 'a'], namespaceSelector: { matchLabels: { env: 'p' } } }, ['c', 'a']))).toEqual(['a', 'b', 'c'])
  })
  it('ignores a stale selection once the selector is gone, and a missing list', () => {
    expect(groupNamespaces(g({ namespaces: ['a'] }, ['c']))).toEqual(['a'])
    expect(groupNamespaces(g({}))).toEqual([])
  })
})

import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { Badge } from './ui'

describe('Badge', () => {
  it('stays on one line next to a long title', () => {
    render(<Badge tone="success">On track</Badge>)
    // A budget whose subtitle ran long squeezed its "On track" badge onto two lines.
    const badge = screen.getByText('On track')
    expect(badge.className).toMatch(/\bwhitespace-nowrap\b/)
    expect(badge.className).toMatch(/\bshrink-0\b/)
  })
})

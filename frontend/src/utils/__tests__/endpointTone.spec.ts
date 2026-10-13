// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest'
import type { Endpoint } from '@/services/endpointApi'
import { endpointStaleBadge, endpointTone } from '../endpointTone'

function ep(overrides: Partial<Endpoint>): Endpoint {
  return { id: 'e1', status: 'up', stale: true, ...overrides } as Endpoint
}

describe('endpointStaleBadge', () => {
  it('labels an offline agent as offline', () => {
    expect(endpointStaleBadge(ep({ agent_offline: true, status: 'up' }))).toEqual({
      text: 'offline',
      title: 'Agent offline · last known: up',
    })
  })

  it('labels a server-probed endpoint without recent checks as stale, with its last known status', () => {
    expect(endpointStaleBadge(ep({ status: 'up' }))).toEqual({
      text: 'stale',
      title: 'Not checked recently · last known: up',
    })
    expect(endpointStaleBadge(ep({ status: 'down' }))).toEqual({
      text: 'stale',
      title: 'Not checked recently · last known: down',
    })
  })

  it('never presents a stale endpoint as up in the row tone', () => {
    expect(endpointTone(ep({ status: 'up' }))).toBe('unknown')
  })
})

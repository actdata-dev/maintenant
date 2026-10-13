// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import type { Endpoint } from '@/services/endpointApi'

const handlers = vi.hoisted(() => ({} as Record<string, (e: MessageEvent) => void>))

vi.mock('@/services/endpointApi', () => ({ listEndpoints: vi.fn() }))
vi.mock('@/services/sseBus', () => ({
  sseBus: {
    on: (name: string, fn: (e: MessageEvent) => void) => { handlers[name] = fn },
    off: vi.fn(), connect: vi.fn(), disconnect: vi.fn(), connected: false, suspended: false,
  },
}))

import { useEndpointsStore } from '../endpoints'

function ep(overrides: Partial<Endpoint>): Endpoint {
  return { id: 'e1', status: 'up', ...overrides } as Endpoint
}

function statusChanged(id: string, newStatus: string) {
  handlers['endpoint.status_changed']!({
    data: JSON.stringify({ endpoint_id: id, new_status: newStatus, timestamp: '2026-10-12T10:00:00Z' }),
  } as MessageEvent)
}

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('endpoints store: a fresh check clears the not-checked-recently flag', () => {
  it('clears stale on a status change when the agent is not offline', () => {
    const store = useEndpointsStore()
    store.connectSSE()
    store.endpoints = [ep({ id: 'a', stale: true, status: 'up' }), ep({ id: 'b', stale: true, status: 'up' })]

    statusChanged('a', 'down')

    expect(store.endpoints[0]!.stale).toBeFalsy()
    expect(store.endpoints[0]!.status).toBe('down')
    expect(store.endpoints[1]!.stale).toBe(true)
  })

  it('keeps stale for an endpoint whose agent is offline', () => {
    const store = useEndpointsStore()
    store.connectSSE()
    store.endpoints = [ep({ id: 'a', stale: true, agent_offline: true })]

    statusChanged('a', 'down')

    expect(store.endpoints[0]!.stale).toBe(true)
  })
})

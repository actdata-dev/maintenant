// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

import type { ChipTone } from '@/components/ui/listFilters'
import type { Endpoint } from '@/services/endpointApi'

const statusTones: Record<Endpoint['status'], ChipTone> = {
  up: 'ok',
  down: 'down',
  degraded: 'warn',
  unknown: 'unknown',
}

/** Row gutter colour. A stale endpoint reports last-known state, not live state. */
export function endpointTone(ep: Endpoint): ChipTone {
  if (ep.stale) return 'unknown'
  return statusTones[ep.status] ?? 'neutral'
}

/** Badge text and tooltip for a stale endpoint, by why its status is not live. */
export function endpointStaleBadge(ep: Endpoint): { text: string; title: string } {
  return ep.agent_offline
    ? { text: 'offline', title: `Agent offline · last known: ${ep.status}` }
    : { text: 'stale', title: `Not checked recently · last known: ${ep.status}` }
}

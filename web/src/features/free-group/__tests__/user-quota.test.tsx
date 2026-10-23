/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { api } from '@/lib/api'
import { MyFreeQuota } from '../components/my-free-quota'
import type { MyFreeGroup } from '../types'

let state: MyFreeGroup
beforeEach(() => {
  state = { enabled: true, eligible: true, models: ['model-a'], display_limits: true, usage: {
    requests: 2, tokens: 100, concurrency: 1, cooldown_ms: 0,
    request_limit: null, token_limit: null, requests_left: null, tokens_left: null,
    model_quotas: [], reset_at: 1,
  } }
  vi.spyOn(api, 'request').mockImplementation(async config => {
    if (config.method === 'delete') return { data: { success: true, data: { state: 'cancelled' } } }
    if (config.url?.endsWith('/queue')) return { data: { success: true, data: [{ id: 'ticket-1', position: 2, state: 'queued', remaining_ms: 10000, cancellable: true }] } }
    return { data: { success: true, data: state } }
  })
})

function renderQuota() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  render(<QueryClientProvider client={client}><MyFreeQuota /></QueryClientProvider>)
  return client
}

it('disabled Free module remains absent from the user page', async () => {
  state.enabled = false
  const client = renderQuota()
  await waitFor(() => expect(client.getQueryState(['my-free-group'])?.status).toBe('success'))
  expect(screen.queryByText('My Free quota')).not.toBeInTheDocument()
})

it('disabled daily quotas display unlimited remaining rather than zero', async () => {
  renderQuota()
  expect(await screen.findAllByText('Unlimited')).toHaveLength(2)
  expect(screen.getByText('Requests today')).toBeInTheDocument()
})

it('canceling a queued request sends only that user ticket to the cancellation API', async () => {
  const user = userEvent.setup()
  renderQuota()
  await user.click(await screen.findByRole('button', { name: 'Cancel queue' }))
  await waitFor(() => expect(vi.mocked(api.request).mock.calls.some(([config]) => config.method === 'delete' && config.url === '/api/user/free-group/queue/ticket-1')).toBe(true))
})

it('a queue retrieval failure is displayed in the existing error component', async () => {
  vi.mocked(api.request).mockImplementation(async config => {
    if (config.url?.endsWith('/queue')) throw new Error('queue unavailable')
    return { data: { success: true, data: state } }
  })
  renderQuota()
  expect(await screen.findByText('Queue status is unavailable')).toBeInTheDocument()
})

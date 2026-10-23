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
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { ConfigEditor } from '../components/config-editor'
import type { FreeConfigRecord } from '../types'

function renderEditor() {
  const config = {
    enabled: false,
    preset: 'standard',
    access_mode: 'all',
    models: { whitelist_enabled: true, models: ['model-a'] },
    request: { max_tokens: { enabled: true, limit: 512, action: 'reject' } },
  }
  const record: FreeConfigRecord = {
    config,
    defaults: structuredClone(config),
    version: 1,
    effective_version: 1,
    updated_by: 1,
    updated_at: 1,
    ready: true,
    strength_score: 50,
    available_models: ['model-a', 'model-b'],
    bindings: ['email'],
    routed_models: ['model-a'],
    redis_configured: true,
    runtime_issue: '',
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ConfigEditor record={record} />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  vi.spyOn(api, 'request').mockImplementation(async (config) => {
    if (config.url?.endsWith('/preview'))
      return {
        data: {
          success: true,
          data: {
            high_risk: true,
            strength_score: 30,
            total_users: 1,
            sampled_users: 1,
            estimated_eligibility_changes: 0,
          },
        },
      }
    return { data: { success: true, data: { version: 2, effective: true } } }
  })
})

it('disabling a restriction disables its inputs and re-enabling retains its limit', async () => {
  const user = userEvent.setup()
  renderEditor()
  const input = screen.getByRole('spinbutton', { name: 'Limit' })
  expect(input).toHaveValue(512)
  await user.click(screen.getByRole('switch', { name: 'Enabled' }))
  expect(input).toBeDisabled()
  await user.click(screen.getByRole('switch', { name: 'Enabled' }))
  expect(input).toBeEnabled()
  expect(input).toHaveValue(512)
})

it('editing a preset threshold marks the configuration as custom', () => {
  renderEditor()
  fireEvent.change(screen.getByRole('spinbutton', { name: 'Limit' }), {
    target: { value: '600' },
  })
  expect(screen.getByRole('combobox', { name: 'Preset' })).toHaveValue('custom')
})

it('enabled-only filtering hides disabled rules', async () => {
  const user = userEvent.setup()
  renderEditor()
  await user.click(screen.getByRole('switch', { name: 'Enabled' }))
  await user.click(
    screen.getByRole('switch', { name: 'Show enabled rules only' })
  )
  expect(
    screen.queryByRole('spinbutton', { name: 'Limit' })
  ).not.toBeInTheDocument()
})

it('select-all and invert update the actual model selection', async () => {
  const user = userEvent.setup()
  renderEditor()
  await user.click(screen.getByRole('button', { name: 'Select all' }))
  expect(screen.getByText('model-b')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Invert selection' }))
  expect(screen.queryByText('model-a')).not.toBeInTheDocument()
  expect(screen.queryByText('model-b')).not.toBeInTheDocument()
})

it('saving a relaxed limit requires the high-risk diff confirmation', async () => {
  const user = userEvent.setup()
  renderEditor()
  fireEvent.change(screen.getByRole('spinbutton', { name: 'Limit' }), {
    target: { value: '600' },
  })
  await user.click(screen.getByRole('button', { name: 'Review and save' }))
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Confirm high-risk Free change',
  })
  expect(
    within(dialog).getByText('request.max_tokens.limit')
  ).toBeInTheDocument()
  expect(
    vi
      .mocked(api.request)
      .mock.calls.some(([config]) => config.method === 'put')
  ).toBe(false)
  await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
  await waitFor(() =>
    expect(
      vi
        .mocked(api.request)
        .mock.calls.some(
          ([config]) =>
            config.method === 'put' &&
            (config.data as { confirm_high_risk?: boolean })
              .confirm_high_risk === true
        )
    ).toBe(true)
  )
})

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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { ModelPriceSources } from '../model-price-sources'

const expr = 'tier("standard", p * 2 + c * 10 + cr * 0.1 + cc * 2.5 + cc1h * 4)'
const change = {
  model_name: 'local',
  expected_version: 'version-1',
  pricing: {
    'billing_setting.billing_mode': 'tiered_expr',
    'billing_setting.billing_expr': expr,
    'billing_setting.price_source': 'model-prices:catalog',
    'billing_setting.price_source_expr': expr,
    'billing_setting.price_source_updated_at': '2026-10-12T10:00:00Z',
  },
}
const snapshot = {
  entries: [
    {
      model_name: 'local',
      version: 'version-1',
      configured: change.pricing,
      effective: change.pricing,
    },
  ],
  options: {},
  empty_version: 'empty',
}
function renderSources() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelPriceSources />
    </QueryClientProvider>
  )
  return client
}
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

it('reviews exact associations and applies the version checked price after confirmation', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: snapshot },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: { changes: [change] } } })
  const patch = vi
    .spyOn(api, 'patch')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  renderSources()
  await user.click(
    screen.getByRole('button', { name: 'Maintained price sources' })
  )
  expect(
    await screen.findByRole('textbox', { name: 'Local model ID' })
  ).toHaveValue('local')
  expect(screen.getByRole('textbox', { name: 'Catalog model ID' })).toHaveValue(
    'catalog'
  )
  await user.click(
    screen.getByRole('button', { name: 'Preview price changes' })
  )
  await screen.findByRole('button', { name: 'Confirm Changes' })
  expect(post).toHaveBeenCalledWith('/api/option/price_source/preview', {
    bindings: { local: 'catalog' },
  })
  expect(patch).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Confirm Changes' }))
  await waitFor(() =>
    expect(patch).toHaveBeenCalledWith('/api/option/model_pricing', {
      changes: [change],
    })
  )
})

it('removes a link while keeping the existing saved price', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: snapshot },
  })
  const detached = {
    ...change,
    pricing: {
      'billing_setting.billing_mode': 'tiered_expr',
      'billing_setting.billing_expr': expr,
    },
  }
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({
      data: { success: true, data: { changes: [detached] } },
    })
  const patch = vi
    .spyOn(api, 'patch')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  renderSources()
  await user.click(
    screen.getByRole('button', { name: 'Maintained price sources' })
  )
  await screen.findByRole('textbox', { name: 'Local model ID' })
  await user.click(screen.getByRole('button', { name: 'Delete row' }))
  await user.click(
    screen.getByRole('button', { name: 'Preview price changes' })
  )
  expect(
    await screen.findByText('Keep saved price; stop automatic updates')
  ).toBeVisible()
  expect(post).toHaveBeenCalledWith('/api/option/price_source/preview', {
    bindings: {},
  })
  expect(patch).not.toHaveBeenCalled()
})

it('retains editable bindings when catalog fetching fails', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: snapshot },
  })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Catalog unavailable' },
  })
  const patch = vi
    .spyOn(api, 'patch')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  renderSources()
  await user.click(
    screen.getByRole('button', { name: 'Maintained price sources' })
  )
  await screen.findByRole('textbox', { name: 'Local model ID' })
  await user.click(
    screen.getByRole('button', { name: 'Preview price changes' })
  )
  expect(await screen.findByText('Catalog unavailable')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Confirm Changes' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('textbox', { name: 'Catalog model ID' })).toHaveValue(
    'catalog'
  )
  expect(patch).not.toHaveBeenCalled()
})

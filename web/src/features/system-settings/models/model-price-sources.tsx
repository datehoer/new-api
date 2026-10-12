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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { JsonEditor } from '@/components/json-editor'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  getModelPricing,
  invalidateModelPricing,
  saveModelPricing,
  type ModelPricingChange,
} from '@/features/model-pricing/api'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import { ConflictConfirmDialog } from './conflict-confirm-dialog'

export function ModelPriceSources() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState('{}')
  const [changes, setChanges] = useState<ModelPricingChange[] | null>(null)
  const pricing = useQuery({
    queryKey: ['model-pricing-config'],
    queryFn: () => getModelPricing(),
    enabled: open,
    refetchOnWindowFocus: false,
  })
  useEffect(() => {
    if (!open || !pricing.data) return
    setDraft(
      JSON.stringify(
        Object.fromEntries(
          pricing.data.entries.flatMap((entry) => {
            const source = entry.configured['billing_setting.price_source']
            return source?.startsWith('model-prices:')
              ? [[entry.model_name, source.slice('model-prices:'.length)]]
              : []
          })
        ),
        null,
        2
      )
    )
  }, [open, pricing.data])
  const preview = useMutation({
    mutationFn: async () => {
      const bindings: unknown = JSON.parse(draft || '{}')
      if (
        !bindings ||
        typeof bindings !== 'object' ||
        Array.isArray(bindings) ||
        Object.entries(bindings).some(
          ([name, source]) =>
            !name.trim() || typeof source !== 'string' || !source.trim()
        )
      ) {
        throw new Error(t('Enter exact local model and catalog model IDs.'))
      }
      const response = await api.post('/api/option/price_source/preview', {
        bindings,
      })
      if (!response.data.success) {
        throw createServerError(
          response.data,
          t('Failed to fetch upstream prices')
        )
      }
      return response.data.data.changes as ModelPricingChange[]
    },
    onSuccess: (next) => {
      if (next.length) setChanges(next)
      else toast.success(t('No price differences found'))
    },
    onError: (error) =>
      handleServerError(error, t('Failed to fetch upstream prices')),
  })
  const save = useMutation({
    mutationFn: () => saveModelPricing(changes ?? []),
    onSuccess: async () => {
      setChanges(null)
      setOpen(false)
      toast.success(t('Prices synced successfully'))
      await invalidateModelPricing(client)
    },
    onError: (error) =>
      handleServerError(error, t('Failed to save model pricing')),
  })
  return (
    <>
      <Button
        variant='outline'
        onClick={() => {
          preview.reset()
          save.reset()
          setOpen(true)
        }}
      >
        {t('Maintained price sources')}
      </Button>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!save.isPending && !preview.isPending) setOpen(next)
        }}
        title={t('Maintained price sources')}
        description={t(
          'Linked models follow model-prices daily. Manual price edits stop automatic updates. Removing a link keeps the saved price.'
        )}
        footer={
          <Button
            onClick={() => preview.mutate()}
            disabled={
              !pricing.data ||
              pricing.isError ||
              preview.isPending ||
              save.isPending
            }
          >
            {t('Preview price changes')}
          </Button>
        }
      >
        {pricing.isPending && <LoadingState />}
        {pricing.isError && (
          <ErrorState
            description={pricing.error.message}
            action={
              <Button onClick={() => void pricing.refetch()}>
                {t('Retry')}
              </Button>
            }
          />
        )}
        {!pricing.isPending && !pricing.isError && (
          <>
            <a
              className='text-sm underline'
              href='https://github.com/datehoer/model-prices'
              target='_blank'
              rel='noreferrer'
            >
              datehoer/model-prices
            </a>
            <JsonEditor
              value={draft}
              onChange={setDraft}
              disabled={preview.isPending || save.isPending}
              keyLabel={t('Local model ID')}
              valueLabel={t('Catalog model ID')}
              keyPlaceholder='claude-sonnet-5-5'
              valuePlaceholder='claude-sonnet-5-5'
              valueType='string'
            />
            {pricing.data?.entries
              .filter(
                (entry) => entry.configured['billing_setting.price_source']
              )
              .map((entry) => (
                <p
                  key={entry.model_name}
                  className='text-muted-foreground text-xs break-all'
                >
                  {entry.model_name} ·{' '}
                  {entry.configured['billing_setting.price_source_updated_at']}
                </p>
              ))}
          </>
        )}
        {preview.isError && <ErrorState description={preview.error.message} />}
      </Dialog>
      <ConflictConfirmDialog
        open={changes !== null}
        onOpenChange={(next) => {
          if (!next && !save.isPending) setChanges(null)
        }}
        conflicts={(changes ?? []).map((change) => ({
          model: change.model_name,
          channel:
            change.pricing['billing_setting.price_source'] ??
            t('Manual pricing'),
          current: String(
            pricing.data?.entries.find(
              (entry) => entry.model_name === change.model_name
            )?.effective['billing_setting.billing_expr'] ?? t('Not configured')
          ),
          newVal: change.pricing['billing_setting.price_source']
            ? String(change.pricing['billing_setting.billing_expr'])
            : t('Keep saved price; stop automatic updates'),
        }))}
        onConfirm={() => save.mutate()}
        isLoading={save.isPending}
        error={save.error?.message}
        onReload={() => {
          setChanges(null)
          save.reset()
          void pricing.refetch()
        }}
      />
    </>
  )
}

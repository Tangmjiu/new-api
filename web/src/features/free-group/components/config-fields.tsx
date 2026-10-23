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
import { useState } from 'react'
import { Controller, useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  SettingsControlGroup,
  SettingsFormGrid,
  SettingsSwitchField,
} from '@/features/system-settings/components/settings-form-layout'

import {
  CHOICE_LABELS,
  CONFIG_LABELS,
  configAt,
  configChoices,
  isConfigObject,
  type FreeConfigForm,
} from '../lib/config'
import type { ConfigObject, ConfigValue } from '../types'

interface FieldsProps {
  value: ConfigObject
  defaults: ConfigObject
  path: string
  models: string[]
  routedModels?: string[]
  bindings: string[]
  enabledOnly: boolean
  disabled?: boolean
}

// The page defines its sections explicitly. Shared field rendering only handles
// individual rules, so adding a backend property cannot expose a new raw form.
const SECTION_FIELDS: Record<string, string[]> = {
  blacklist: ['enabled', 'user_ids', 'recovery'],
  admission: [
    'registration',
    'bindings',
    'captcha',
    'ip_accounts',
    'fingerprint_accounts',
  ],
  models: [
    'per_model_quota_enabled',
    'per_model_concurrency_enabled',
    'per_model_limits',
  ],
  concurrency: [
    'user',
    'token',
    'ip',
    'model',
    'global',
    'user_share',
    'lease_ttl_enabled',
    'lease_ttl_seconds',
    'renew',
  ],
  queue: ['enabled', 'max_wait_seconds', 'max_length', 'poll_millis'],
  rate: [
    'rpm',
    'tpm',
    'hourly_requests',
    'hourly_tokens',
    'daily_requests',
    'daily_tokens',
    'monthly_requests',
    'monthly_tokens',
    'timezone',
    'idle_credit',
  ],
  request: [
    'max_tokens',
    'prompt_tokens',
    'body_size_kb',
    'streaming',
    'multimodal',
    'timeout_seconds',
    'output_input_ratio',
    'parameters',
  ],
  anti_abuse: [
    'ip_active_accounts',
    'fingerprint_active_accounts',
    'ip_aggregate_rate',
    'prompt_hash_repeat',
    'machine_interval',
    'night_high_frequency',
    'short_prompt',
    'failure_rate',
    'request_retention',
    'actions',
  ],
  penalty: [
    'cooldown_on_limit',
    'cooldown_minutes',
    'exponential',
    'multiplier',
    'maximum_minutes',
    'reject_during_cooldown',
    'whitelist_enabled',
    'whitelist_users',
    'whitelist_ips',
  ],
  notify: [
    'user_limit_display',
    'error_template',
    'webhook_enabled',
    'webhook_url',
    'email_enabled',
    'site_enabled',
  ],
}

function ConfigMap(props: {
  value: ConfigObject
  onChange: (value: ConfigObject) => void
  models: string[]
  parameters: boolean
  disabled: boolean
  disabledKeys?: string[]
}) {
  const { t } = useTranslation()
  const [newKey, setNewKey] = useState('')
  const keys = props.parameters
    ? ['min', 'max']
    : ['daily_requests', 'daily_tokens', 'concurrency']
  return (
    <div className='space-y-3'>
      {!Object.keys(props.value).length && (
        <p className='text-muted-foreground text-sm'>
          {t('No overrides. Global limits apply.')}
        </p>
      )}
      {Object.entries(props.value).map(([name, raw]) => (
        <div key={name} className='space-y-2 rounded-lg border p-3'>
          <div className='flex items-center justify-between gap-2'>
            <span className='font-mono text-sm break-all'>{name}</span>
            <Button
              type='button'
              size='sm'
              variant='ghost'
              disabled={props.disabled}
              onClick={() => {
                const next = { ...props.value }
                delete next[name]
                props.onChange(next)
              }}
            >
              {t('Delete')}
            </Button>
          </div>
          <SettingsFormGrid>
            {keys.map((key) => (
              <div key={key} className='space-y-1'>
                <Label htmlFor={`override-${name}-${key}`}>
                  {t(CONFIG_LABELS[key] ?? key)}
                </Label>
                <Input
                  id={`override-${name}-${key}`}
                  type='number'
                  step={props.parameters ? 'any' : 1}
                  min={props.parameters ? undefined : 0}
                  disabled={props.disabled || props.disabledKeys?.includes(key)}
                  placeholder='0'
                  value={isConfigObject(raw) ? String(raw[key] ?? 0) : '0'}
                  onChange={(event) =>
                    props.onChange({
                      ...props.value,
                      [name]: {
                        ...(isConfigObject(raw) ? raw : {}),
                        [key]: Number(event.target.value),
                      },
                    })
                  }
                />
              </div>
            ))}
          </SettingsFormGrid>
        </div>
      ))}
      <div className='flex flex-wrap items-center gap-2'>
        {props.parameters ? (
          <Input
            aria-label={t('Parameter name')}
            value={newKey}
            disabled={props.disabled}
            onChange={(event) => setNewKey(event.target.value)}
          />
        ) : (
          <NativeSelect
            aria-label={t('Models')}
            value={newKey}
            disabled={props.disabled}
            onChange={(event) => setNewKey(event.target.value)}
          >
            <NativeSelectOption value=''>
              {t('Select a model')}
            </NativeSelectOption>
            {props.models
              .filter((name) => !(name in props.value))
              .map((name) => (
                <NativeSelectOption key={name} value={name}>
                  {name}
                </NativeSelectOption>
              ))}
          </NativeSelect>
        )}
        <Button
          type='button'
          variant='outline'
          disabled={
            props.disabled ||
            !newKey.trim() ||
            newKey in props.value ||
            ['__proto__', 'prototype', 'constructor'].includes(newKey)
          }
          onClick={() => {
            props.onChange({
              ...props.value,
              [newKey.trim()]: Object.fromEntries(keys.map((key) => [key, 0])),
            })
            setNewKey('')
          }}
        >
          {t('Add override')}
        </Button>
      </div>
      <p className='text-muted-foreground text-xs'>
        {t('A zero model override inherits the global value.')}
      </p>
    </div>
  )
}

function ConfigField(
  props: FieldsProps & { fieldKey: string; fieldValue: ConfigValue }
) {
  const { t } = useTranslation()
  const form = useFormContext<FreeConfigForm>()
  const path = `${props.path}.${props.fieldKey}`
  const defaultValue = configAt(props.defaults, path)
  const label = t(CONFIG_LABELS[props.fieldKey] ?? props.fieldKey)
  const value = props.fieldValue
  const disabled =
    props.disabled ||
    (props.value.enabled === false && props.fieldKey !== 'enabled') ||
    (path === 'notify.webhook_url' && props.value.webhook_enabled === false) ||
    (path === 'concurrency.lease_ttl_seconds' &&
      props.value.lease_ttl_enabled === false) ||
    (path.startsWith('penalty.whitelist_') &&
      path !== 'penalty.whitelist_enabled' &&
      props.value.whitelist_enabled === false)
  if (
    props.enabledOnly &&
    (value === false || (isConfigObject(value) && value.enabled === false))
  )
    return null
  if (
    props.fieldKey === 'window_seconds' &&
    /^rate\.(hourly|daily|monthly)_/.test(props.path)
  )
    return null
  if (
    ['max_wait_seconds', 'max_queue_length'].includes(props.fieldKey) &&
    props.value.strategy === 'reject'
  )
    return null
  if (
    isConfigObject(value) &&
    path !== 'models.per_model_limits' &&
    path !== 'request.parameters.ranges'
  ) {
    return (
      <SettingsControlGroup>
        <ConfigFields
          {...props}
          path={path}
          value={value}
          disabled={!!disabled}
        />
      </SettingsControlGroup>
    )
  }
  return (
    <Controller
      control={form.control}
      name={`config.${path}`}
      render={({ field }) => {
        const onChange = (next: unknown) => {
          field.onChange(next)
          form.setValue('config.preset', 'custom', { shouldDirty: true })
        }
        if (typeof value === 'boolean')
          return (
            <SettingsSwitchField
              controlId={`free-${path}`}
              label={label}
              checked={field.value === true}
              onCheckedChange={onChange}
              disabled={!!disabled}
            />
          )
        let input
        if (isConfigObject(value)) {
          input = (
            <ConfigMap
              value={isConfigObject(field.value) ? field.value : {}}
              onChange={onChange}
              disabledKeys={
                path === 'models.per_model_limits'
                  ? ['daily_requests', 'daily_tokens', 'concurrency'].filter(
                      (key) =>
                        key === 'concurrency'
                          ? props.value.per_model_concurrency_enabled === false
                          : props.value.per_model_quota_enabled === false
                    )
                  : undefined
              }
              models={props.models}
              parameters={path === 'request.parameters.ranges'}
              disabled={!!disabled}
            />
          )
        } else if (Array.isArray(value)) {
          const selected = Array.isArray(field.value)
            ? field.value.map(String)
            : []
          const options =
            path === 'admission.bindings.required' ? props.bindings : selected
          const numeric =
            path.endsWith('user_ids') || path.endsWith('whitelist_users')
          input = (
            <MultiSelect
              id={`free-${path}`}
              options={options.map((item) => ({ value: item, label: item }))}
              selected={selected}
              onChange={(items) => {
                if (
                  numeric &&
                  items.some(
                    (id) => !Number.isSafeInteger(Number(id)) || Number(id) < 1
                  )
                ) {
                  toast.error(t('Invalid user ID'))
                  return
                }
                onChange(numeric ? items.map(Number) : items)
              }}
              disabled={!!disabled}
              allowCreate={path !== 'admission.bindings.required'}
              maxVisibleChips={4}
            />
          )
        } else {
          const choices = configChoices(path)
          input = choices ? (
            <NativeSelect
              id={`free-${path}`}
              value={String(field.value ?? '')}
              disabled={!!disabled}
              onChange={(event) => onChange(event.target.value)}
            >
              {choices.map((choice) => (
                <NativeSelectOption key={choice} value={choice}>
                  {t(CHOICE_LABELS[choice] ?? choice)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          ) : (
            <Input
              id={`free-${path}`}
              name={field.name}
              ref={field.ref}
              onBlur={field.onBlur}
              disabled={!!disabled}
              type={typeof value === 'number' ? 'number' : 'text'}
              step={typeof value === 'number' ? 'any' : undefined}
              value={String(field.value ?? '')}
              placeholder={String(defaultValue ?? '')}
              onChange={(event) =>
                onChange(
                  typeof value === 'number'
                    ? Number(event.target.value || defaultValue || 0)
                    : event.target.value || defaultValue || ''
                )
              }
            />
          )
        }
        return (
          <div
            data-settings-form-span={
              Array.isArray(value) || isConfigObject(value) ? 'full' : undefined
            }
            className='min-w-0 space-y-2'
          >
            <Label htmlFor={`free-${path}`}>{label}</Label>
            {input}
          </div>
        )
      }}
    />
  )
}

export function ConfigFields(props: FieldsProps) {
  const { t } = useTranslation()
  const form = useFormContext<FreeConfigForm>()
  const fields = SECTION_FIELDS[props.path] ?? Object.keys(props.value)
  const hasSwitch = typeof props.value.enabled === 'boolean'
  const heading =
    CONFIG_LABELS[props.path.split('.').at(-1) ?? ''] ?? props.path
  return (
    <div className='space-y-4'>
      {hasSwitch && (
        <Controller
          control={form.control}
          name={`config.${props.path}.enabled`}
          render={({ field }) => (
            <SettingsSwitchField
              controlId={`free-${props.path}-enabled`}
              label={t(heading)}
              checked={field.value === true}
              disabled={props.disabled}
              onCheckedChange={(value) => {
                field.onChange(value)
                form.setValue('config.preset', 'custom', { shouldDirty: true })
              }}
              description={t('Disabled rules do not restrict requests.')}
            />
          )}
        />
      )}
      {!hasSwitch && !SECTION_FIELDS[props.path] && <Label>{t(heading)}</Label>}
      <SettingsFormGrid>
        {fields
          .filter((key) => key !== 'enabled' && props.value[key] !== undefined)
          .map((key) => (
            <ConfigField
              key={key}
              {...props}
              fieldKey={key}
              fieldValue={props.value[key]}
            />
          ))}
      </SettingsFormGrid>
    </div>
  )
}

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
import { Info, ChevronDown } from 'lucide-react'
import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { SettingsControlGroup, SettingsControlChildren, SettingsFormGrid, SettingsSwitchField } from '@/features/system-settings/components/settings-form-layout'
import { CHOICE_LABELS, CONFIG_LABELS, configAt, configChoices, isConfigObject, type FreeConfigForm } from '../lib/config'
import type { ConfigObject } from '../types'

interface FieldsProps {
  value: ConfigObject
  defaults: ConfigObject
  path: string
  models: string[]
  bindings: string[]
  enabledOnly: boolean
  disabled?: boolean
}

function ConfigMap(props: { value: ConfigObject; onChange: (value: ConfigObject) => void; models: string[]; parameters: boolean; disabled: boolean; disabledKeys?: string[] }) {
  const { t } = useTranslation()
  const [newKey, setNewKey] = useState('')
  const keys = props.parameters ? ['min', 'max'] : ['daily_requests', 'daily_tokens', 'concurrency']
  return <div className='space-y-3'>
    {!Object.keys(props.value).length && <p className='text-muted-foreground text-sm'>{t('No overrides. Global limits apply.')}</p>}
    {Object.entries(props.value).map(([name, raw]) => <div key={name} className='space-y-2 rounded-lg border p-3'>
      <div className='flex items-center justify-between gap-2'><span className='break-all font-mono text-sm'>{name}</span><Button type='button' size='sm' variant='ghost' disabled={props.disabled} onClick={() => { const next = { ...props.value }; delete next[name]; props.onChange(next) }}>{t('Delete')}</Button></div>
      <SettingsFormGrid>{keys.map(key => <div key={key} className='space-y-1'><Label htmlFor={`override-${name}-${key}`}>{t(CONFIG_LABELS[key] ?? key)}</Label><Input id={`override-${name}-${key}`} type='number' step={props.parameters ? 'any' : 1} min={props.parameters ? undefined : 0} disabled={props.disabled || props.disabledKeys?.includes(key)} placeholder='0' value={isConfigObject(raw) ? String(raw[key] ?? 0) : '0'} onChange={event => props.onChange({ ...props.value, [name]: { ...(isConfigObject(raw) ? raw : {}), [key]: Number(event.target.value) } })} /></div>)}</SettingsFormGrid>
    </div>)}
    <div className='flex flex-wrap items-center gap-2'>
      {props.parameters ? <Input aria-label={t('Parameter name')} value={newKey} disabled={props.disabled} onChange={event => setNewKey(event.target.value)} /> : <NativeSelect aria-label={t('Models')} value={newKey} disabled={props.disabled} onChange={event => setNewKey(event.target.value)}><NativeSelectOption value=''>{t('Select a model')}</NativeSelectOption>{props.models.filter(name => !(name in props.value)).map(name => <NativeSelectOption key={name} value={name}>{name}</NativeSelectOption>)}</NativeSelect>}
      <Button type='button' variant='outline' disabled={props.disabled || !newKey.trim() || newKey in props.value || ['__proto__', 'prototype', 'constructor'].includes(newKey)} onClick={() => { props.onChange({ ...props.value, [newKey.trim()]: Object.fromEntries(keys.map(key => [key, 0])) }); setNewKey('') }}>{t('Add override')}</Button>
    </div>
    <p className='text-muted-foreground text-xs'>{t('A zero model override inherits the global value.')}</p>
  </div>
}

export function ConfigFields(props: FieldsProps) {
  const { t } = useTranslation()
  const form = useFormContext<FreeConfigForm>()
  const ownEnabled = props.value.enabled
  const disabled = props.disabled || (typeof ownEnabled === 'boolean' && !ownEnabled)
  return <div className='space-y-4'>
    {typeof ownEnabled === 'boolean' && <Controller control={form.control} name={`config.${props.path}.enabled`} render={({ field }) => <SettingsSwitchField controlId={`free-${props.path}`} label={t('Enabled')} checked={field.value === true} disabled={props.disabled} onCheckedChange={value => { field.onChange(value); form.setValue('config.preset', 'custom', { shouldDirty: true }) }} description={t('Disabled rules do not restrict requests.')} />} />}
    <SettingsFormGrid>
      {Object.entries(props.value).filter(([key]) => key !== 'enabled' && !(key === 'window_seconds' && /^rate\.(hourly|daily|monthly)_/.test(props.path))).map(([key, value]) => {
        const path = props.path ? `${props.path}.${key}` : key
        const defaultValue = configAt(props.defaults, path)
        const label = t(CONFIG_LABELS[key] ?? key)
        if (isConfigObject(value) && path !== 'models.per_model_limits' && path !== 'request.parameters.ranges') {
          if (props.enabledOnly && value.enabled === false) return null
          return <SettingsControlGroup key={path}>
            <Collapsible defaultOpen>
              <CollapsibleTrigger className='flex w-full items-center justify-between gap-2 py-2 text-left text-sm font-medium'>{label}<ChevronDown aria-hidden='true' className='size-4' /></CollapsibleTrigger>
              <CollapsibleContent><SettingsControlChildren><ConfigFields {...props} path={path} value={value} disabled={disabled} /></SettingsControlChildren></CollapsibleContent>
            </Collapsible>
          </SettingsControlGroup>
        }
        const itemDisabled = disabled || (path === 'models.models' && props.value.whitelist_enabled === false) || (path === 'notify.webhook_url' && props.value.webhook_enabled === false) || (path === 'concurrency.lease_ttl_seconds' && props.value.lease_ttl_enabled === false)
        if (props.enabledOnly && typeof value === 'boolean' && !value) return null
        return <div key={path} data-settings-form-span={typeof value === 'object' ? 'full' : undefined} className='min-w-0 space-y-2'>
          <div className='flex items-center gap-2'><Label htmlFor={`free-${path}`}>{label}</Label><Tooltip><TooltipTrigger render={<Button type='button' variant='ghost' size='icon' aria-label={t('Help')} />}><Info aria-hidden='true' className='size-4' /></TooltipTrigger><TooltipContent>{path === 'models.models' ? t('Only models selected here define the Free whitelist. Channel matching uses existing channel settings.') : t('Disabled rules do not restrict requests.')}</TooltipContent></Tooltip></div>
          <Controller control={form.control} name={`config.${path}`} render={({ field }) => {
            const onChange = (next: unknown) => { field.onChange(next); form.setValue('config.preset', 'custom', { shouldDirty: true }) }
            if (typeof value === 'boolean') return <SettingsSwitchField controlId={`free-${path}`} label={label} checked={field.value === true} onCheckedChange={onChange} disabled={itemDisabled} />
            if (isConfigObject(value)) return <ConfigMap value={isConfigObject(field.value) ? field.value : {}} onChange={onChange} disabledKeys={path === 'models.per_model_limits' ? ['daily_requests', 'daily_tokens', 'concurrency'].filter(key => key === 'concurrency' ? props.value.per_model_concurrency_enabled === false : props.value.per_model_quota_enabled === false) : undefined} models={props.models} parameters={path === 'request.parameters.ranges'} disabled={!!itemDisabled} />
            if (Array.isArray(value)) {
              const selected = Array.isArray(field.value) ? field.value.map(String) : []
              let options = selected.map(item => ({ value: item, label: item }))
              if (path === 'models.models') options = props.models.map(item => ({ value: item, label: item }))
              if (path === 'admission.bindings.required') options = props.bindings.map(item => ({ value: item, label: item }))
              const numeric = path.endsWith('user_ids') || path.endsWith('whitelist_users')
              return <div className='space-y-2'><MultiSelect id={`free-${path}`} options={options} selected={selected} onChange={items => { if (numeric && items.some(id => !Number.isSafeInteger(Number(id)) || Number(id) < 1)) { toast.error(t('Invalid user ID')); return }; onChange(numeric ? items.map(Number) : items) }} disabled={itemDisabled} allowCreate={path !== 'models.models' && path !== 'admission.bindings.required'} maxVisibleChips={6} />{path === 'models.models' && <div className='flex flex-wrap gap-2'><Button type='button' variant='outline' size='sm' disabled={itemDisabled} onClick={() => onChange(props.models)}>{t('Select all')}</Button><Button type='button' variant='outline' size='sm' disabled={itemDisabled} onClick={() => onChange(props.models.filter(item => !selected.includes(item)))}>{t('Invert selection')}</Button></div>}</div>
            }
            const choices = configChoices(path)
            if (choices) return <NativeSelect id={`free-${path}`} value={String(field.value ?? '')} disabled={itemDisabled} onChange={event => onChange(event.target.value)}>{choices.map(choice => <NativeSelectOption key={choice} value={choice}>{t(CHOICE_LABELS[choice] ?? choice)}</NativeSelectOption>)}</NativeSelect>
            return <Input id={`free-${path}`} name={field.name} ref={field.ref} onBlur={field.onBlur} disabled={itemDisabled} type={typeof value === 'number' ? 'number' : 'text'} step={typeof value === 'number' ? 'any' : undefined} value={String(field.value ?? '')} placeholder={String(defaultValue ?? '')} onChange={event => onChange(typeof value === 'number' ? Number(event.target.value || defaultValue || 0) : event.target.value || defaultValue || '')} />
          }} />
          <p className='text-muted-foreground break-all text-xs'>{t('Default: {{value}}', { value: JSON.stringify(defaultValue ?? null) })} · <code>{path}</code></p>
        </div>
      })}
    </SettingsFormGrid>
  </div>
}

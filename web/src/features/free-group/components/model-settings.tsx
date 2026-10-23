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
import { Link } from '@tanstack/react-router'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import { Accordion } from '@/components/ui/accordion'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { SettingsAccordion } from '@/features/system-settings/components/settings-accordion'
import { SettingsSwitchField } from '@/features/system-settings/components/settings-form-layout'

import { CHOICE_LABELS, type FreeConfigForm } from '../lib/config'
import type { ConfigObject, ConfigValue, FreeConfigRecord } from '../types'
import { ConfigFields } from './config-fields'

export function ModelSettings(props: {
  record: FreeConfigRecord
  enabledOnly: boolean
}) {
  const { t } = useTranslation()
  const form = useFormContext<FreeConfigForm>()
  const models = useWatch({
    control: form.control,
    name: 'config.models',
  }) as ConfigObject
  const selected = Array.isArray(models.models) ? models.models.map(String) : []
  const available = props.record.available_models ?? []
  const routed = props.record.routed_models ?? []
  const unrouted = selected.filter((name) => !routed.includes(name))
  const restricted = models.whitelist_enabled === true
  function change(key: string, value: ConfigValue) {
    form.setValue(
      'config.models',
      { ...models, [key]: value },
      { shouldDirty: true }
    )
    form.setValue('config.preset', 'custom', { shouldDirty: true })
  }
  return (
    <div className='space-y-5'>
      <SettingsSwitchField
        controlId='free-model-restriction'
        label={t('Limit available models')}
        checked={restricted}
        onCheckedChange={(value) => change('whitelist_enabled', value)}
        description={t(
          'Choose which models Free API keys can use. Channel routing is configured on the Channels page.'
        )}
      />
      <div className='space-y-2'>
        <Label htmlFor='free-available-models'>
          {t('Available Free models')}
        </Label>
        <MultiSelect
          id='free-available-models'
          disabled={!restricted}
          options={[...new Set([...available, ...selected])]
            .sort()
            .map((name) => ({
              value: name,
              label: name,
              hint: routed.includes(name)
                ? t('Channel ready')
                : t('No Free channel'),
            }))}
          selected={selected}
          onChange={(value) => change('models', value)}
          placeholder={t('Select a model')}
          maxVisibleChips={4}
        />
        <div className='flex flex-wrap gap-2'>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={!restricted}
            onClick={() => change('models', available)}
          >
            {t('Select all')}
          </Button>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={!restricted}
            onClick={() =>
              change(
                'models',
                available.filter((name) => !selected.includes(name))
              )
            }
          >
            {t('Invert selection')}
          </Button>
          <Link
            to='/channels'
            className='text-primary inline-flex items-center text-sm underline underline-offset-4'
          >
            {t('Manage channels')}
          </Link>
        </div>
      </div>
      {restricted &&
        models.outside_policy === 'reject' &&
        selected.length === 0 && (
          <Alert variant='destructive'>
            <AlertDescription>
              {t(
                'No Free models selected. Requests will be rejected until you select and save at least one model.'
              )}
            </AlertDescription>
          </Alert>
        )}
      {restricted && unrouted.length > 0 && (
        <Alert>
          <AlertDescription>
            {t(
              'These models have no enabled channel assigned to free: {{models}}. Edit their channel groups on the Channels page.',
              { models: unrouted.join(', ') }
            )}
          </AlertDescription>
        </Alert>
      )}
      {!restricted && (
        <Alert variant='destructive'>
          <AlertDescription>
            {t(
              'Model restriction is off. All models routed to free are allowed. Saving requires risk confirmation.'
            )}
          </AlertDescription>
        </Alert>
      )}
      <Accordion>
        <SettingsAccordion
          value='advanced-model-settings'
          title={t('Advanced settings')}
        >
          <div className='space-y-5'>
            <div className='space-y-2'>
              <Label htmlFor='free-outside-model-policy'>
                {t('Requests outside the whitelist')}
              </Label>
              <NativeSelect
                id='free-outside-model-policy'
                disabled={!restricted}
                value={String(models.outside_policy)}
                onChange={(event) =>
                  change('outside_policy', event.target.value)
                }
              >
                {['reject', 'allow'].map((value) => (
                  <NativeSelectOption key={value} value={value}>
                    {t(CHOICE_LABELS[value])}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </div>
            <ConfigFields
              value={models}
              path='models'
              defaults={props.record.defaults}
              models={available}
              bindings={props.record.bindings}
              enabledOnly={props.enabledOnly}
            />
          </div>
        </SettingsAccordion>
      </Accordion>
    </div>
  )
}

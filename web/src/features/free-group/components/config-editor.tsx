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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Accordion } from '@/components/ui/accordion'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { SettingsAccordion } from '@/features/system-settings/components/settings-accordion'
import { SettingsCard } from '@/features/system-settings/components/settings-card'
import {
  SettingsSwitchField,
  SettingsFormGrid,
} from '@/features/system-settings/components/settings-form-layout'
import { SettingsPageFormActions } from '@/features/system-settings/components/settings-page-context'

import {
  getFreePreset,
  previewFreeConfig,
  reviewFreeConfig,
  saveFreeConfig,
} from '../api'
import {
  CONFIG_LABELS,
  PRESETS,
  CHOICE_LABELS,
  diffConfig,
  freeConfigSchema,
  isConfigObject,
  type FreeConfigForm,
} from '../lib/config'
import type {
  ConfigObject,
  ConfigValue,
  FreeConfigRecord,
  FreePreview,
} from '../types'
import { ConfigFields } from './config-fields'
import { ModelSettings } from './model-settings'

const SECTIONS = [
  'admission',
  'models',
  'concurrency',
  'rate',
  'request',
  'anti_abuse',
  'penalty',
  'notify',
]

export function ConfigEditor(props: { record: FreeConfigRecord }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const form = useForm<FreeConfigForm>({
    resolver: zodResolver(freeConfigSchema),
    defaultValues: { config: props.record.config, summary: '' },
  })
  const config = useWatch({
    control: form.control,
    name: 'config',
  }) as ConfigObject
  const [enabledOnly, setEnabledOnly] = useState(false)
  const [confirmation, setConfirmation] = useState<'save' | 'preset' | null>(
    null
  )
  const [riskAccepted, setRiskAccepted] = useState(false)
  const [candidate, setCandidate] = useState<ConfigObject | null>(null)
  const [preview, setPreview] = useState<FreePreview | null>(null)
  const changes = diffConfig(
    confirmation === 'preset' ? config : props.record.config,
    candidate ?? config
  )
  const dirty = diffConfig(props.record.config, config).length > 0
  const save = useMutation({
    mutationFn: () =>
      saveFreeConfig(
        props.record.version,
        config,
        form.getValues('summary'),
        riskAccepted
      ),
    onSuccess: async (result) => {
      setConfirmation(null)
      if (result.effective) toast.success(t('Free configuration is effective'))
      else toast.warning(t('Free configuration saved; activation pending'))
      await Promise.all([
        client.invalidateQueries({ queryKey: ['free-group'] }),
        client.invalidateQueries({ queryKey: ['my-free-group'] }),
        client.invalidateQueries({ queryKey: ['user-groups'] }),
        client.invalidateQueries({ queryKey: ['channel-groups'] }),
      ])
    },
  })
  const preset = useMutation({
    mutationFn: (name: string) => getFreePreset(config, name),
    onSuccess: (result) => {
      setCandidate(result.config)
      setRiskAccepted(result.high_risk)
      setConfirmation('preset')
    },
  })
  const impact = useMutation({
    mutationFn: () => previewFreeConfig(config),
    onSuccess: setPreview,
  })
  const review = useMutation({
    mutationFn: () => reviewFreeConfig(config),
    onSuccess: (result) => {
      setRiskAccepted(result.high_risk)
      setCandidate(null)
      setConfirmation('save')
    },
  })
  const confirmSave = form.handleSubmit(() => review.mutate())
  function setValue(key: string, value: ConfigValue) {
    form.setValue(`config.${key}`, value, { shouldDirty: true })
    form.setValue('config.preset', 'custom', { shouldDirty: true })
  }
  function reset() {
    form.reset({ config: props.record.config, summary: '' })
    setPreview(null)
  }
  function fields(path: string) {
    const value = config[path]
    if (!isConfigObject(value)) return null
    return (
      <ConfigFields
        path={path}
        value={value}
        defaults={props.record.defaults}
        models={props.record.available_models ?? []}
        bindings={props.record.bindings}
        enabledOnly={enabledOnly}
      />
    )
  }
  function displayValue(value: ConfigValue | undefined) {
    if (typeof value === 'boolean') return t(value ? 'Enabled' : 'Disabled')
    if (Array.isArray(value)) return value.join(', ') || t('None')
    if (typeof value === 'string')
      return t(
        CHOICE_LABELS[value] ?? PRESETS[value as keyof typeof PRESETS] ?? value
      )
    return value === undefined || value === null
      ? '—'
      : typeof value === 'object'
        ? JSON.stringify(value)
        : String(value)
  }
  return (
    <Form {...form}>
      <form
        onSubmit={(event) => {
          event.preventDefault()
          void confirmSave()
        }}
        className='space-y-4'
      >
        <SettingsPageFormActions
          onSave={() => void confirmSave()}
          onReset={reset}
          isSaving={save.isPending || review.isPending}
          isSaveDisabled={!dirty}
          isResetDisabled={!dirty}
        />
        <SettingsCard
          title={t('Free module')}
          description={t(
            'Free access has independent limits and never uses group multipliers.'
          )}
        >
          <div className='space-y-5'>
            <SettingsSwitchField
              controlId='free-master-enabled'
              label={t('Enable Free module')}
              checked={config.enabled === true}
              onCheckedChange={(value) => setValue('enabled', value)}
            />
            <SettingsFormGrid>
              <div className='space-y-2'>
                <Label htmlFor='free-preset'>{t('Preset')}</Label>
                <NativeSelect
                  id='free-preset'
                  value={String(config.preset)}
                  disabled={preset.isPending}
                  onChange={(event) => {
                    if (event.target.value === 'custom') {
                      setValue('preset', 'custom')
                      return
                    }
                    preset.mutate(event.target.value)
                  }}
                >
                  {Object.entries(PRESETS).map(([key, label]) => (
                    <NativeSelectOption key={key} value={key}>
                      {t(label)}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </div>
              <div className='space-y-2'>
                <Label htmlFor='free-access-mode'>{t('Access mode')}</Label>
                <NativeSelect
                  id='free-access-mode'
                  value={String(config.access_mode)}
                  onChange={(event) =>
                    setValue('access_mode', event.target.value)
                  }
                >
                  {['all', 'condition', 'apply', 'invite'].map((value) => (
                    <NativeSelectOption key={value} value={value}>
                      {t(CHOICE_LABELS[value])}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </div>
            </SettingsFormGrid>
          </div>
        </SettingsCard>
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <SettingsSwitchField
            controlId='free-enabled-only'
            label={t('Show enabled rules only')}
            checked={enabledOnly}
            onCheckedChange={setEnabledOnly}
          />
          <Button
            type='button'
            variant='outline'
            disabled={impact.isPending}
            onClick={() => impact.mutate()}
          >
            {t('Preview impact')}
          </Button>
        </div>
        {preview && (
          <Alert>
            <AlertDescription>
              {t(
                'Strength: {{score}}/100. Estimated eligibility changes: {{count}} (sample {{sample}}/{{total}}). Usage and IP limits depend on future requests.',
                {
                  score: preview.strength_score,
                  count: preview.estimated_eligibility_changes,
                  sample: preview.sampled_users,
                  total: preview.total_users,
                }
              )}
            </AlertDescription>
          </Alert>
        )}
        <SettingsCard title={t('Configuration')}>
          <Accordion defaultValue={['models']} multiple>
            {SECTIONS.map((section) => (
              <SettingsAccordion
                key={section}
                value={section}
                title={t(CONFIG_LABELS[section])}
              >
                {section === 'models' ? (
                  <ModelSettings
                    record={props.record}
                    enabledOnly={enabledOnly}
                  />
                ) : (
                  <div className='space-y-5'>
                    {section === 'admission' ? (
                      <>
                        {fields('blacklist')}
                        {config.access_mode === 'condition' &&
                          fields('admission')}
                        {config.access_mode === 'apply' &&
                          isConfigObject(config.admission) &&
                          isConfigObject(config.admission.captcha) && (
                            <ConfigFields
                              path='admission.captcha'
                              value={config.admission.captcha}
                              defaults={props.record.defaults}
                              models={[]}
                              bindings={props.record.bindings}
                              enabledOnly={enabledOnly}
                            />
                          )}
                        {['apply', 'invite'].includes(
                          String(config.access_mode)
                        ) && (
                          <p className='text-muted-foreground text-sm'>
                            {t('Manage access in the Free membership tab.')}
                          </p>
                        )}
                      </>
                    ) : (
                      fields(section)
                    )}
                    {section === 'concurrency' && fields('queue')}
                  </div>
                )}
              </SettingsAccordion>
            ))}
          </Accordion>
        </SettingsCard>
        <div className='space-y-2'>
          <Label htmlFor='free-change-summary'>{t('Change summary')}</Label>
          <Input
            id='free-change-summary'
            maxLength={512}
            {...form.register('summary')}
          />
        </div>
        <ConfirmDialog
          open={confirmation !== null}
          onOpenChange={(open) => {
            if (!open) {
              setConfirmation(null)
              setCandidate(null)
            }
          }}
          title={t(
            riskAccepted
              ? 'Confirm high-risk Free change'
              : 'Review Free configuration changes'
          )}
          destructive={riskAccepted}
          isLoading={save.isPending}
          desc={t(
            riskAccepted
              ? 'This change relaxes Free protections. Confirm explicit administrator permission; the change will be audited as high risk.'
              : 'The following changes take effect immediately after saving.'
          )}
          handleConfirm={() => {
            if (confirmation === 'preset' && candidate) {
              form.setValue('config', candidate, { shouldDirty: true })
              setCandidate(null)
              setConfirmation(null)
            } else {
              save.mutate()
            }
          }}
        >
          <div className='max-h-80 overflow-auto'>
            <StaticDataTable
              data={changes}
              getRowKey={(row) => row.path}
              columns={[
                {
                  id: 'path',
                  header: t('Rule'),
                  cell: (row) => (
                    <span className='text-xs'>
                      {row.path
                        .split('.')
                        .map((key) => t(CONFIG_LABELS[key] ?? key))
                        .join(' / ')}
                    </span>
                  ),
                },
                {
                  id: 'before',
                  header: t('Before'),
                  cell: (row) => (
                    <span className='text-xs break-all'>
                      {displayValue(row.before)}
                    </span>
                  ),
                },
                {
                  id: 'after',
                  header: t('After'),
                  cell: (row) => (
                    <span className='text-xs break-all'>
                      {displayValue(row.after)}
                    </span>
                  ),
                },
              ]}
            />
          </div>
        </ConfirmDialog>
      </form>
    </Form>
  )
}

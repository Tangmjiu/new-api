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
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/empty-state'
import { Turnstile } from '@/components/turnstile'
import { Button } from '@/components/ui/button'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { SettingsCard } from '@/features/system-settings/components/settings-card'
import { handleServerError } from '@/lib/handle-server-error'
import { StaticDataTable } from '@/components/data-table'
import { formatNumber } from '@/lib/format'
import { toIntlLocale } from '@/i18n/languages'
import { translateServerText } from '@/lib/server-error-message'
import { cancelFreeQueue, freeRequest, getFreeQueues, getMyFreeGroup } from '../api'

export function MyFreeQuota(props: { compact?: boolean }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const client = useQueryClient()
  const [captcha,setCaptcha] = useState('')
  const expire = useCallback(()=>setCaptcha(''),[])
  const query = useQuery({queryKey:['my-free-group'],queryFn:getMyFreeGroup,refetchInterval:15000,retry:false,meta:{errorToast:false}})
  const state = query.data
  const queues = useQuery({queryKey:['my-free-group','queue'],queryFn:getFreeQueues,enabled:state?.enabled===true&&state.eligible&&!props.compact,refetchInterval:3000,retry:false,meta:{errorToast:false}})
  const refresh = async()=>{await Promise.all([client.invalidateQueries({queryKey:['my-free-group']}),client.invalidateQueries({queryKey:['user-groups']})])}
  const apply = useMutation({mutationFn:()=>freeRequest(`user/free-group/${state?.access_mode==='apply'?'apply':'verify'}?turnstile=${encodeURIComponent(captcha)}`,'post',{}),onSuccess:refresh,onError:error=>{setCaptcha('');handleServerError(error)}})
  const cancel = useMutation({mutationFn:cancelFreeQueue,onSuccess:refresh})
  if (!state?.enabled) return null
  const usage = state.usage
  return <SettingsCard title={t('My Free quota')} description={t('Free access has independent limits and never uses group multipliers.')}>
    <div className='space-y-3'>
      {query.isError&&<Alert variant='destructive'><AlertDescription>{t('Free capacity service is unavailable. Requests fail closed.')}</AlertDescription></Alert>}
      {!state.eligible && <div className='space-y-3'><EmptyState title={t('Free access is not available for this account')} description={state.error?translateServerText(t,state.error):undefined}/>
        {state.captcha_required&&state.captcha_site_key&&<Turnstile siteKey={state.captcha_site_key} onVerify={setCaptcha} onExpire={expire}/>}
        {(state.access_mode==='apply'||state.captcha_required)&&<Button disabled={apply.isPending||state.member_status==='pending'||!!state.captcha_required&&!captcha} onClick={()=>apply.mutate()}>{t(state.member_status==='pending'?'Awaiting approval':'Request Free access')}</Button>}
        {apply.isError&&<Alert variant='destructive'><AlertDescription>{t('Free access request failed. Please retry.')}</AlertDescription></Alert>}
      </div>}
      {state.eligible&&<>
        <p className='text-muted-foreground break-words text-sm'>{t('Available models')}: {state.models.join(', ')||t('No models available')}</p>
        {state.display_limits&&usage&&<dl className='grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-3'>
          {[
            ['Requests today',formatNumber(usage.requests,locale)],['Tokens today',formatNumber(usage.tokens,locale)],
            ['Requests remaining',usage.requests_left===null?t('Unlimited'):formatNumber(usage.requests_left,locale)],['Tokens remaining',usage.tokens_left===null?t('Unlimited'):formatNumber(usage.tokens_left,locale)],
            ['Current concurrency',formatNumber(usage.concurrency,locale)],['Cooldown remaining',t('{{count}} seconds',{count:Math.ceil(usage.cooldown_ms/1000)})],
          ].map(([label,value])=><div key={label}><dt className='text-muted-foreground'>{t(label)}</dt><dd className='font-medium tabular-nums'>{value}</dd></div>)}
        </dl>}
        {state.display_limits&&!!usage?.model_quotas?.length&&<StaticDataTable data={usage.model_quotas} getRowKey={row=>row.model} columns={[
          {id:'model',header:t('Model'),cell:row=>row.model},
          {id:'requests',header:t('Requests remaining'),cell:row=>row.requests_left===null?t('Unlimited'):formatNumber(row.requests_left,locale)},
          {id:'tokens',header:t('Tokens remaining'),cell:row=>row.tokens_left===null?t('Unlimited'):formatNumber(row.tokens_left,locale)},
        ]}/>}
      </>}
      {!!queues.data?.length&&<div className='space-y-2'><h4 className='text-sm font-medium'>{t('Queued requests')}</h4>{queues.data.map(ticket=><div key={ticket.id} className='flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3'><span className='text-sm'>{t('Queue position {{position}}, up to {{seconds}} seconds remaining',{position:ticket.position,seconds:Math.ceil((ticket.remaining_ms??0)/1000)})}</span><Button type='button' size='sm' variant='outline' disabled={!ticket.cancellable||cancel.isPending} onClick={()=>cancel.mutate(ticket.id)}>{t('Cancel queue')}</Button></div>)}</div>}
      {queues.isError&&<Alert variant='destructive'><AlertDescription>{t('Queue status is unavailable')}</AlertDescription></Alert>}
    </div>
  </SettingsCard>
}

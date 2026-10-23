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
import type { ColumnDef } from '@tanstack/react-table'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { DataTablePage, StaticDataTable, useDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { SettingsCard } from '@/features/system-settings/components/settings-card'
import { freeRequest, rollbackFreeConfig } from '../api'
import { CHOICE_LABELS, PRESETS, diffConfig, type ConfigDiff } from '../lib/config'
import type { ConfigObject, FreeAudit, FreeMember, FreeVersion } from '../types'

const STATUS_LABELS: Record<string,string> = {approved:'Allowed',pending:'Pending',blocked:'Blocked',removed:'Removed'}
const SCOPE_LABELS: Record<string,string> = {user:'User ID',ip:'IP address',fingerprint:'Key Fingerprint'}

type AdminRow = Partial<FreeAudit & FreeMember & FreeVersion>
type Kind = 'audits' | 'members' | 'versions'
export function AdminRecords(props: { kind: Kind; version: number; config: ConfigObject }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [pagination,setPagination] = useState({ pageIndex:0,pageSize:20 })
  const [draft,setDraft] = useState({ user_id:'',ip:'',rule:'',start_timestamp:'',end_timestamp:'',status:'' })
  const [filters,setFilters] = useState<Record<string,string>>({})
  const [rollback,setRollback] = useState<{ version:number; changes:ConfigDiff[] } | null>(null)
  const params = new URLSearchParams({ ...filters,offset:String(pagination.pageIndex*pagination.pageSize),limit:String(pagination.pageSize) })
  const endpoint = props.kind === 'versions' ? 'config/versions' : props.kind
  const query = useQuery({ queryKey:['free-group',props.kind,params.toString()],queryFn:()=>freeRequest<{items:AdminRow[];total:number}>(`free-group/${endpoint}?${params}`) })
  const preview = useMutation({mutationFn:(version:number)=>freeRequest<{config:ConfigObject}>(`free-group/config/versions/${version}`),onSuccess:(result,version)=>setRollback({version,changes:diffConfig(props.config,result.config)})})
  const revert = useMutation({mutationFn:(version:number)=>rollbackFreeConfig(version,props.version),onSuccess:async result=>{setRollback(null);toast.success(t(result.effective?'Free configuration is effective':'Free configuration saved; activation pending'));await client.invalidateQueries({queryKey:['free-group']})}})
  let columns:ColumnDef<AdminRow>[]
  if (props.kind === 'audits') { columns = [
    {accessorKey:'created_at',header:t('Time'),cell:({row})=>new Date((row.original.created_at??0)*1000).toISOString()},
    {accessorKey:'user_id',header:t('User ID')},{accessorKey:'ip',header:'IP'},
    {accessorKey:'rule_name',header:t('Rule')},{accessorKey:'action',header:t('Action'),cell:({row})=>t(CHOICE_LABELS[row.original.action ?? ''] ?? row.original.action ?? '')},{accessorKey:'config_version',header:t('Version')},
    {accessorKey:'snapshot',header:t('Details'),cell:({row})=><details><summary>{t('Details')}</summary><pre className='max-w-sm overflow-auto text-xs whitespace-pre-wrap break-all'>{row.original.snapshot}</pre><code>{row.original.fingerprint}</code></details>},
  ] } else if (props.kind === 'members') { columns = [
    {accessorKey:'user_id',header:t('User ID')},{accessorKey:'status',header:t('Status'),cell:({row})=>t(STATUS_LABELS[row.original.status ?? ''] ?? row.original.status ?? '')},{accessorKey:'source',header:t('Source')},{accessorKey:'reason',header:t('Reason')},{accessorKey:'updated_by',header:t('Modified by')},
  ] } else { columns = [
    {accessorKey:'version',header:t('Version')},{accessorKey:'preset',header:t('Preset'),cell:({row})=>t(PRESETS[row.original.preset as keyof typeof PRESETS] ?? row.original.preset ?? '')},{accessorKey:'summary',header:t('Change summary')},{accessorKey:'updated_by',header:t('Modified by')},
    {accessorKey:'high_risk',header:t('High risk'),cell:({row})=>t(row.original.high_risk?'Yes':'No')},
    {id:'rollback',header:t('Rollback'),cell:({row})=><Button variant='outline' size='sm' disabled={row.original.version===props.version||preview.isPending} onClick={()=>preview.mutate(row.original.version ?? 0)}>{t('Preview rollback')}</Button>},
  ] }
  const { table } = useDataTable({columns,data:query.data?.items??[],totalCount:query.data?.total??0,pagination,onPaginationChange:setPagination,manualPagination:true,manualFiltering:true,enableSorting:false,enableRowSelection:false})
  return <div className='space-y-4'>
    <form className='flex flex-wrap items-end gap-2' onSubmit={event=>{event.preventDefault();const next:Record<string,string>={};for(const [key,value] of Object.entries(draft)){if(value) next[key]=key.endsWith('_timestamp')?String(Math.floor(new Date(value).getTime()/1000)):value};setFilters(next);setPagination(previous=>({...previous,pageIndex:0}))}}>
      {props.kind === 'audits' && ['user_id','ip','rule','start_timestamp','end_timestamp'].map(key=><div key={key} className='space-y-1'><Label htmlFor={`free-filter-${key}`}>{t(({user_id:'User ID',ip:'IP address',rule:'Rule',start_timestamp:'Start time',end_timestamp:'End time'} as Record<string,string>)[key])}</Label><Input id={`free-filter-${key}`} type={key.endsWith('_timestamp')?'datetime-local':'text'} value={draft[key as keyof typeof draft]} onChange={event=>setDraft(previous=>({...previous,[key]:event.target.value}))}/></div>)}
      {props.kind==='members'&&<div className='space-y-1'><Label htmlFor='free-member-filter'>{t('Status')}</Label><NativeSelect id='free-member-filter' value={draft.status} onChange={event=>setDraft(previous=>({...previous,status:event.target.value}))}>{['','pending','approved','blocked','removed'].map(value=><NativeSelectOption key={value} value={value}>{t(STATUS_LABELS[value] ?? 'All')}</NativeSelectOption>)}</NativeSelect></div>}
      {props.kind!=='versions'&&<Button type='submit' variant='outline'>{t('Search')}</Button>}
      <Button variant='outline' type='button' onClick={()=>void query.refetch()}>{t('Refresh')}</Button>
    </form>
    {query.isError&&<Alert variant='destructive'><AlertDescription>{t('Failed to load audit records')}</AlertDescription></Alert>}
    <DataTablePage table={table} columns={columns} isLoading={query.isPending} isFetching={query.isFetching} emptyTitle={t('No records')} paginationInFooter={false} className='min-h-80' />
    <ConfirmDialog open={!!rollback} onOpenChange={open=>{if(!open)setRollback(null)}} title={t('Confirm high-risk Free change')} desc={t('Rollback creates a new audited version. Review all changes before confirming.')} destructive isLoading={revert.isPending} handleConfirm={()=>{if(rollback)revert.mutate(rollback.version)}}>
      <div className='max-h-80 overflow-auto'><StaticDataTable data={rollback?.changes??[]} columns={[{id:'path',header:t('Rule'),cell:row=><code>{row.path}</code>},{id:'before',header:t('Before'),cell:row=><span className='break-all'>{JSON.stringify(row.before)}</span>},{id:'after',header:t('After'),cell:row=><span className='break-all'>{JSON.stringify(row.after)}</span>}]} /></div>
    </ConfirmDialog>
  </div>
}

export function FreeManualActions() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [member,setMember] = useState({user_id:'',status:'approved',reason:''})
  const [penalty,setPenalty] = useState({scope:'user',target:'',action:'release',minutes:10,reason:''})
  const [pending,setPending] = useState<'member'|'delete'|'penalty'|null>(null)
  const mutate = useMutation({mutationFn:async()=>{
    if(pending==='penalty') return freeRequest('free-group/penalties','post',penalty)
    return freeRequest(`free-group/members/${encodeURIComponent(member.user_id)}`,pending==='delete'?'delete':'put',pending==='delete'?undefined:{status:member.status,reason:member.reason})
  },onSuccess:async()=>{setPending(null);toast.success(t('Saved successfully'));await client.invalidateQueries({queryKey:['free-group']})}})
  return <div className='grid gap-4 lg:grid-cols-2'>
    <SettingsCard title={t('Free membership')}><form className='space-y-3' onSubmit={event=>{event.preventDefault();setPending('member')}}>
      <Label htmlFor='free-member-user'>{t('User ID')}</Label><Input id='free-member-user' required type='number' min={1} step={1} value={member.user_id} onChange={event=>setMember({...member,user_id:event.target.value})}/>
      <Label htmlFor='free-member-status'>{t('Status')}</Label><NativeSelect id='free-member-status' value={member.status} onChange={event=>setMember({...member,status:event.target.value})}>{['approved','pending','blocked'].map(value=><NativeSelectOption key={value} value={value}>{t(STATUS_LABELS[value])}</NativeSelectOption>)}</NativeSelect>
      <Label htmlFor='free-member-reason'>{t('Reason')}</Label><Input id='free-member-reason' required maxLength={255} value={member.reason} onChange={event=>setMember({...member,reason:event.target.value})}/>
      <div className='flex flex-wrap gap-2'><Button type='submit'>{t('Save')}</Button><Button type='button' variant='outline' disabled={!member.user_id} onClick={()=>setPending('delete')}>{t('Remove membership entry')}</Button></div>
    </form></SettingsCard>
    <SettingsCard title={t('Manual cooldown and release')}><form className='space-y-3' onSubmit={event=>{event.preventDefault();setPending('penalty')}}>
      <Label htmlFor='free-penalty-scope'>{t('Scope')}</Label><NativeSelect id='free-penalty-scope' value={penalty.scope} onChange={event=>setPenalty({...penalty,scope:event.target.value})}>{['user','ip','fingerprint'].map(value=><NativeSelectOption key={value} value={value}>{t(SCOPE_LABELS[value])}</NativeSelectOption>)}</NativeSelect>
      <Label htmlFor='free-penalty-target'>{t('User ID, IP or fingerprint hash')}</Label><Input id='free-penalty-target' required value={penalty.target} onChange={event=>setPenalty({...penalty,target:event.target.value})}/>
      <Label htmlFor='free-penalty-action'>{t('Action')}</Label><NativeSelect id='free-penalty-action' value={penalty.action} onChange={event=>setPenalty({...penalty,action:event.target.value})}><NativeSelectOption value='release'>{t('Release cooldown')}</NativeSelectOption><NativeSelectOption value='cooldown'>{t('Cooldown')}</NativeSelectOption></NativeSelect>
      <Label htmlFor='free-penalty-minutes'>{t('Cooldown duration (minutes)')}</Label><Input id='free-penalty-minutes' type='number' min={1} max={525600} disabled={penalty.action==='release'} value={penalty.minutes} onChange={event=>setPenalty({...penalty,minutes:Number(event.target.value)})}/>
      <Label htmlFor='free-penalty-reason'>{t('Reason')}</Label><Input id='free-penalty-reason' required maxLength={255} value={penalty.reason} onChange={event=>setPenalty({...penalty,reason:event.target.value})}/><Button type='submit'>{t('Apply')}</Button>
    </form></SettingsCard>
    <ConfirmDialog open={pending!==null} onOpenChange={open=>{if(!open)setPending(null)}} title={t('Confirm Free access change')} desc={t('This action changes only Free access and is recorded in the audit log.')} destructive isLoading={mutate.isPending} handleConfirm={()=>mutate.mutate()}/>
  </div>
}

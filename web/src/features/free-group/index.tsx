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
import { useQuery } from '@tanstack/react-query'
import { Activity, ShieldAlert, Users, ListOrdered, Ban } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { StatCard } from '@/features/dashboard/components/ui/stat-card'
import { SettingsPageFrame } from '@/features/system-settings/components/settings-page'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { getFreeConfig, getFreeDashboard } from './api'
import { AdminRecords, FreeManualActions } from './components/admin-records'
import { ConfigEditor } from './components/config-editor'

export function FreeGroupSettings() {
  const { t, i18n } = useTranslation()
  const [tab, setTab] = useState('config')
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const config = useQuery({
    queryKey: ['free-group', 'config'],
    queryFn: getFreeConfig,
    refetchOnWindowFocus: false,
  })
  const record = config.data
  const dashboard = useQuery({
    queryKey: ['free-group', 'dashboard'],
    queryFn: getFreeDashboard,
    refetchInterval: 10000,
    retry: false,
    enabled:
      tab === 'monitor' && record?.config.enabled === true && record.ready,
    meta: { errorToast: false },
  })
  return (
    <SettingsPageFrame title={t('Free group settings')}>
      {config.isPending && <LoadingState />}
      {config.isError && <ErrorState onRetry={() => void config.refetch()} />}
      {record && (
        <div className='space-y-4'>
          <div className='text-muted-foreground flex flex-wrap items-center gap-3 text-xs'>
            <Badge
              variant={
                !record.config.enabled
                  ? 'outline'
                  : record.ready
                    ? 'secondary'
                    : 'destructive'
              }
            >
              {t(
                !record.config.enabled
                  ? 'Disabled'
                  : record.ready
                    ? 'Ready'
                    : 'Unavailable'
              )}
            </Badge>
            <span>
              {t('Version')}: {record.version}
            </span>
            <span>
              {t('Strength')}: {record.strength_score}/100
            </span>
            <time dateTime={new Date(record.updated_at * 1000).toISOString()}>
              {new Date(record.updated_at * 1000).toLocaleString(locale)}
            </time>
            <span>
              {t('Modified by')}: {record.updated_by}
            </span>
          </div>
          {(!record.redis_configured || !record.ready) && (
            <Alert variant={record.config.enabled ? 'destructive' : 'default'}>
              <AlertDescription>
                <div className='space-y-2'>
                  <p>
                    {t(
                      !record.redis_configured
                        ? 'Redis is not configured. Set REDIS_CONN_STRING on the New API service and restart it.'
                        : record.runtime_issue ||
                            'Free configuration could not be activated. Check the Redis connection and server logs.'
                    )}
                  </p>
                  <p>
                    {t(
                      'You can save settings while the service is unavailable. Free requests resume only after activation succeeds.'
                    )}
                  </p>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    onClick={() => void config.refetch()}
                  >
                    {t('Refresh')}
                  </Button>
                </div>
              </AlertDescription>
            </Alert>
          )}
          <Tabs value={tab} onValueChange={(value) => setTab(String(value))}>
            <TabsList variant='line' className='max-w-full overflow-x-auto'>
              <TabsTrigger value='config'>{t('Configuration')}</TabsTrigger>
              <TabsTrigger value='monitor'>{t('Usage')}</TabsTrigger>
              <TabsTrigger value='versions'>{t('Version history')}</TabsTrigger>
              <TabsTrigger value='audits'>{t('Audit logs')}</TabsTrigger>
              <TabsTrigger value='members'>{t('Free membership')}</TabsTrigger>
            </TabsList>
            <TabsContent value='config' className='pt-4'>
              <ConfigEditor key={record.version} record={record} />
            </TabsContent>
            <TabsContent value='monitor' className='space-y-4 pt-4'>
              {dashboard.isError && (
                <ErrorState onRetry={() => void dashboard.refetch()} />
              )}
              <div className='grid gap-4 sm:grid-cols-2 xl:grid-cols-5'>
                {[
                  {
                    key: 'concurrency',
                    title: 'Global Free concurrency',
                    icon: Activity,
                  },
                  { key: 'requests', title: 'Requests today', icon: Users },
                  {
                    key: 'rejected',
                    title: 'Rejected today',
                    icon: ShieldAlert,
                  },
                  {
                    key: 'queued',
                    title: 'Queued requests',
                    icon: ListOrdered,
                  },
                  { key: 'blocked_users', title: 'Blocked users', icon: Ban },
                ].map((stat) => (
                  <Card key={stat.key}>
                    <CardContent className='pt-4'>
                      <StatCard
                        title={t(stat.title)}
                        value={
                          dashboard.data
                            ? formatNumber(
                                dashboard.data[stat.key as 'requests'],
                                locale
                              )
                            : '—'
                        }
                        description={t('Free module')}
                        icon={stat.icon}
                        loading={dashboard.isLoading}
                        error={dashboard.isError}
                      />
                    </CardContent>
                  </Card>
                ))}
              </div>
            </TabsContent>
            <TabsContent value='versions' className='pt-4'>
              <AdminRecords
                kind='versions'
                version={record.version}
                config={record.config}
              />
            </TabsContent>
            <TabsContent value='audits' className='pt-4'>
              <AdminRecords
                kind='audits'
                version={record.version}
                config={record.config}
              />
            </TabsContent>
            <TabsContent value='members' className='space-y-4 pt-4'>
              <FreeManualActions />
              <AdminRecords
                kind='members'
                version={record.version}
                config={record.config}
              />
            </TabsContent>
          </Tabs>
        </div>
      )}
    </SettingsPageFrame>
  )
}

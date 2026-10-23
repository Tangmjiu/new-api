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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  ConfigObject,
  FreeConfigRecord,
  FreeDashboard,
  FreePreview,
  FreeVersion,
  MyFreeGroup,
  FreeQueueStatus,
} from './types'

export async function freeRequest<T>(
  path: string,
  method: 'get' | 'post' | 'put' | 'delete' = 'get',
  data?: unknown
): Promise<T> {
  const response = await api.request<{
    success: boolean
    message?: string
    data: T
  }>({ url: `/api/${path}`, method, data })
  return requireServerSuccess(response.data).data
}
export const getFreeConfig = () =>
  freeRequest<FreeConfigRecord>('free-group/config')
export const getMyFreeGroup = () => freeRequest<MyFreeGroup>('user/free-group')
export const getFreeDashboard = () =>
  freeRequest<FreeDashboard>('free-group/dashboard')
export const getFreeVersions = () =>
  freeRequest<{ items: FreeVersion[]; total: number }>(
    'free-group/config/versions?limit=200'
  )
export const getFreePreset = (config: ConfigObject, preset: string) =>
  freeRequest<{
    config: ConfigObject
    strength_score: number
    high_risk: boolean
  }>('free-group/config/preset', 'post', { config, preset })
export const previewFreeConfig = (config: ConfigObject) =>
  freeRequest<FreePreview>('free-group/config/preview', 'post', { config })
export const reviewFreeConfig = (config: ConfigObject) =>
  freeRequest<{ high_risk: boolean }>(
    'free-group/config/preview?summary_only=true',
    'post',
    { config }
  )
export const saveFreeConfig = (
  version: number,
  config: ConfigObject,
  summary: string,
  confirmHighRisk: boolean
) =>
  freeRequest<{ version: number; effective: boolean }>(
    'free-group/config',
    'put',
    { version, config, summary, confirm_high_risk: confirmHighRisk }
  )
export const rollbackFreeConfig = (version: number, expectedVersion: number) =>
  freeRequest<{ version: number; effective: boolean }>(
    `free-group/config/versions/${version}/rollback`,
    'post',
    { expected_version: expectedVersion, confirm_high_risk: true }
  )
export const getFreeQueue = (id: string) =>
  freeRequest<FreeQueueStatus>(
    `user/free-group/queue/${encodeURIComponent(id)}`
  )
export const cancelFreeQueue = (id: string) =>
  freeRequest<FreeQueueStatus>(
    `user/free-group/queue/${encodeURIComponent(id)}`,
    'delete'
  )

export const getFreeQueues = () =>
  freeRequest<FreeQueueStatus[]>('user/free-group/queue')

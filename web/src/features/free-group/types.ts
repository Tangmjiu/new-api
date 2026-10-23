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
export type ConfigValue =
  | string
  | number
  | boolean
  | null
  | ConfigObject
  | ConfigValue[]
export interface ConfigObject {
  [key: string]: ConfigValue
}
export interface FreeConfigRecord {
  config: ConfigObject
  defaults: ConfigObject
  version: number
  effective_version: number
  updated_by: number
  updated_at: number
  ready: boolean
  strength_score: number
  available_models: string[]
  routed_models: string[]
  redis_configured: boolean
  runtime_issue: string
  bindings: string[]
}
export interface FreeUsage {
  requests: number
  tokens: number
  concurrency: number
  cooldown_ms: number
  request_limit: number | null
  token_limit: number | null
  requests_left: number | null
  tokens_left: number | null
  model_quotas: Array<{
    model: string
    requests: number
    tokens: number
    request_limit: number | null
    token_limit: number | null
    requests_left: number | null
    tokens_left: number | null
  }>
  reset_at: number
}
export interface MyFreeGroup {
  enabled: boolean
  eligible: boolean
  access_mode?: string
  member_status?: string
  error?: string
  models: string[]
  display_limits?: boolean
  usage: FreeUsage | null
  config_version?: number
  captcha_required?: boolean
  captcha_site_key?: string
}
export interface FreeVersion {
  version: number
  preset: string
  summary: string
  high_risk: boolean
  updated_by: number
  created_at: number
}
export interface FreeAudit {
  id: number
  user_id: number
  ip: string
  fingerprint: string
  rule_name: string
  threshold: string
  snapshot: string
  action: string
  source: string
  config_version: number
  created_at: number
}
export interface FreeMember {
  user_id: number
  status: string
  source: string
  reason: string
  updated_by: number
  updated_at: number
}
export interface FreeDashboard {
  concurrency: number
  requests: number
  tokens: number
  queued: number
  rejected: number
  blocked_users: number
  metrics: Record<string, string>
  config_version: number
  ready: boolean
}
export interface FreePreview {
  total_users: number
  sampled_users: number
  eligible_before: number
  eligible_after: number
  estimated_eligibility_changes: number
  limits_changed: boolean
  high_risk: boolean
  strength_score: number
  runtime_limits_not_estimated: boolean
}
export interface FreeQueueStatus {
  id: string
  position: number
  state: string
  cancellable: boolean
  remaining_ms?: number
}

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
import { z } from 'zod'
import type { ConfigObject, ConfigValue } from '../types'

export const freeConfigSchema = z.object({ config: z.record(z.string(), z.unknown()).refine(value => z.json().safeParse(value).success, 'Invalid configuration'), summary: z.string().max(512) })
export type FreeConfigForm = z.infer<typeof freeConfigSchema>
export function isConfigObject(value: unknown): value is ConfigObject {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}
export function configAt(config: ConfigObject, path: string): ConfigValue | undefined {
  let value: ConfigValue = config
  for (const key of path.split('.')) {
    if (!isConfigObject(value)) return undefined
    value = value[key]
  }
  return value
}
export interface ConfigDiff { path: string; before: ConfigValue | undefined; after: ConfigValue | undefined }
export function diffConfig(before: ConfigObject, after: ConfigObject, prefix = ''): ConfigDiff[] {
  const changes: ConfigDiff[] = []
  for (const key of new Set([...Object.keys(before), ...Object.keys(after)])) {
    const path = prefix ? `${prefix}.${key}` : key
    if (isConfigObject(before[key]) && isConfigObject(after[key])) {
      changes.push(...diffConfig(before[key], after[key], path))
    } else if (JSON.stringify(before[key]) !== JSON.stringify(after[key])) {
      changes.push({ path, before: before[key], after: after[key] })
    }
  }
  return changes
}
export const PRESETS = {
  very_strict: 'Very strict', strict: 'Strict', standard: 'Standard', relaxed: 'Relaxed', very_relaxed: 'Very relaxed', custom: 'Custom',
}
export const CONFIG_LABELS: Record<string, string> = {
  enabled: 'Enabled', access_mode: 'Access mode', blacklist: 'Free blacklist', user_ids: 'User IDs', recovery: 'Removal recovery',
  admission: 'Free admission', registration: 'Registration age (hours)', bindings: 'Required account bindings', required: 'Required bindings', captcha: 'Human verification', method: 'Verification method', fingerprint_accounts: 'Accounts per fingerprint', ip_accounts: 'Accounts per IP',
  models: 'Models', whitelist_enabled: 'Model whitelist', outside_policy: 'Requests outside the whitelist', per_model_quota_enabled: 'Per-model daily quotas', per_model_concurrency_enabled: 'Per-model concurrency', per_model_limits: 'Model overrides',
  concurrency: 'Concurrency', user: 'Per user', token: 'Per API key', ip: 'Per IP', model: 'Per model', global: 'Global Free concurrency', user_share: 'User share of global concurrency', percent: 'Percentage', lease_ttl_enabled: 'Lease TTL protection', lease_ttl_seconds: 'Lease TTL (seconds)', renew: 'Stream lease renewal', heartbeat_seconds: 'Heartbeat (seconds)',
  limit: 'Limit', strategy: 'Capacity strategy', max_wait_seconds: 'Maximum wait (seconds)', max_queue_length: 'Maximum queue length', max_length: 'Maximum queue length', poll_millis: 'Queue poll interval (ms)',
  rate: 'Frequency and quotas', rpm: 'Requests per minute', tpm: 'Tokens per minute', burst: 'Burst capacity', hourly_requests: 'Hourly requests', hourly_tokens: 'Hourly tokens', daily_requests: 'Daily requests', daily_tokens: 'Daily tokens', monthly_requests: 'Monthly requests', monthly_tokens: 'Monthly tokens', window_seconds: 'Window (seconds)', timezone: 'Quota reset timezone', idle_credit: 'Idle credits', accrual_per_hour: 'Credits per idle hour', maximum: 'Maximum accumulated credits',
  request: 'Request restrictions', max_tokens: 'Maximum output tokens', prompt_tokens: 'Maximum prompt tokens', body_size_kb: 'Maximum body size (KB)', parameters: 'Parameter ranges', ranges: 'Additional parameter ranges', temperature_min: 'Minimum temperature', temperature_max: 'Maximum temperature', streaming: 'Streaming', multimodal: 'Images, files and multimodal', allowed: 'Allowed', timeout_seconds: 'Request timeout (seconds)', output_input_ratio: 'Output / input token ratio', action: 'Action',
  anti_abuse: 'Abuse protection', ip_active_accounts: 'Active accounts per IP', fingerprint_active_accounts: 'Active accounts per fingerprint', ip_aggregate_rate: 'Aggregated IP frequency', prompt_hash_repeat: 'Repeated prompt hash', machine_interval: 'Machine-like intervals', night_high_frequency: 'Night-time frequency', short_prompt: 'Very short prompts', failure_rate: 'Consecutive failures', start_hour: 'Start hour', end_hour: 'End hour', prompt_token_minimum: 'Minimum prompt tokens', threshold: 'Trigger threshold', sample_count: 'Sample count', request_retention: 'Request traces', retention_days: 'Retention (days)', actions: 'Response actions', observe: 'Observe', slowdown: 'Temporary slowdown', cooldown: 'Cooldown', remove: 'Remove Free access', interval_seconds: 'Minimum interval (seconds)',
  penalty: 'Penalties and cooldown', cooldown_on_limit: 'Consecutive limit violations', cooldown_minutes: 'Cooldown duration (minutes)', exponential: 'Exponential cooldown', multiplier: 'Cooldown multiplier', maximum_minutes: 'Maximum cooldown (minutes)', reject_during_cooldown: 'Reject during cooldown', whitelist_users: 'Exempt user IDs', whitelist_ips: 'Exempt IP addresses',
  notify: 'Notifications and display', webhook_enabled: 'Webhook alerts', webhook_url: 'Webhook URL', email_enabled: 'Email alerts', site_enabled: 'Console alerts', user_limit_display: 'Show user limits', error_template: 'Rate-limit message template', queue: 'Free request queue', min: 'Minimum', max: 'Maximum',
}
export function configChoices(path: string): string[] | undefined {
  if (path === 'access_mode') return ['all', 'condition', 'apply', 'invite']
  if (path.endsWith('.recovery')) return ['auto', 'manual']
  if (path.endsWith('.outside_policy')) return ['reject', 'allow']
  if (path.endsWith('.strategy')) return ['reject', 'queue']
  if (path.endsWith('.method')) return ['', 'turnstile']
  if (path.endsWith('.action')) return path === 'request.max_tokens.action' ? ['reject', 'truncate'] : ['observe', 'alert', 'slowdown', 'cooldown', 'remove']
  return undefined
}
export const CHOICE_LABELS: Record<string,string> = { all:'All signed-in users', condition:'Automatic conditions', apply:'Application and approval', invite:'Invitation only', auto:'Automatic', manual:'Manual', reject:'Reject', allow:'Allow', queue:'Queue', truncate:'Truncate', observe:'Observe', alert:'Alert notification', slowdown:'Temporary slowdown', cooldown:'Cooldown', remove:'Remove Free access', turnstile:'Cloudflare Turnstile', '':'None' }

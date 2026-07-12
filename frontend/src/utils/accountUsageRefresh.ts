import type { Account } from '@/types'

type UsageRefreshAccount = Pick<
  Account,
  'id' | 'platform' | 'type' | 'updated_at' | 'last_used_at' | 'rate_limit_reset_at' | 'extra'
>

const normalizeUsageRefreshValue = (value: unknown): string => {
  if (value == null) return ''
  return String(value)
}

export const buildOpenAIUsageRefreshKey = (account: UsageRefreshAccount): string => {
  if (account.platform !== 'openai' || account.type !== 'oauth') {
    return ''
  }

  const extra = account.extra ?? {}
  return [
    account.id,
    account.updated_at,
    account.last_used_at,
    account.rate_limit_reset_at,
    extra.codex_usage_updated_at,
    extra.codex_5h_used_percent,
    extra.codex_5h_reset_at,
    extra.codex_5h_reset_after_seconds,
    extra.codex_5h_window_minutes,
    extra.codex_7d_used_percent,
    extra.codex_7d_reset_at,
    extra.codex_7d_reset_after_seconds,
    extra.codex_7d_window_minutes
  ].map(normalizeUsageRefreshValue).join('|')
}

/** Same idea as OpenAI codex snapshot keys: only re-fetch /usage when list row signals change. */
export const buildGrokUsageRefreshKey = (account: UsageRefreshAccount): string => {
  if (account.platform !== 'grok' || account.type !== 'oauth') {
    return ''
  }

  const extra = account.extra ?? {}
  const billing =
    extra.grok_billing_snapshot && typeof extra.grok_billing_snapshot === 'object'
      ? (extra.grok_billing_snapshot as Record<string, unknown>)
      : {}

  return [
    account.id,
    account.updated_at,
    account.last_used_at,
    account.rate_limit_reset_at,
    billing.fetched_at,
    billing.updated_at,
    billing.usage_percent,
    billing.used_percent,
    billing.used_cents,
    billing.included_used_cents,
    billing.monthly_limit_cents,
    billing.period_end,
    billing.billing_period_end,
    billing.plan
  ].map(normalizeUsageRefreshValue).join('|')
}

export const buildAccountUsageRefreshKey = (account: UsageRefreshAccount): string => {
  return buildOpenAIUsageRefreshKey(account) || buildGrokUsageRefreshKey(account)
}

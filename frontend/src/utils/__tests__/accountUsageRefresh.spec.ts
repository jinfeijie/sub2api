import { describe, expect, it } from 'vitest'
import {
  buildAccountUsageRefreshKey,
  buildGrokUsageRefreshKey,
  buildOpenAIUsageRefreshKey
} from '../accountUsageRefresh'

describe('buildOpenAIUsageRefreshKey', () => {
  it('会在 codex 快照变化时生成不同 key', () => {
    const base = {
      id: 1,
      platform: 'openai',
      type: 'oauth',
      updated_at: '2026-03-07T10:00:00Z',
      last_used_at: '2026-03-07T09:59:00Z',
      extra: {
        codex_usage_updated_at: '2026-03-07T10:00:00Z',
        codex_5h_used_percent: 0,
        codex_7d_used_percent: 0
      }
    } as any

    const next = {
      ...base,
      extra: {
        ...base.extra,
        codex_usage_updated_at: '2026-03-07T10:01:00Z',
        codex_5h_used_percent: 100
      }
    }

    expect(buildOpenAIUsageRefreshKey(base)).not.toBe(buildOpenAIUsageRefreshKey(next))
  })

  it('会在 last_used_at 变化时生成不同 key', () => {
    const base = {
      id: 3,
      platform: 'openai',
      type: 'oauth',
      updated_at: '2026-03-07T10:00:00Z',
      last_used_at: '2026-03-07T10:00:00Z',
      extra: {
        codex_usage_updated_at: '2026-03-07T10:00:00Z',
        codex_5h_used_percent: 12,
        codex_7d_used_percent: 24
      }
    } as any

    const next = {
      ...base,
      last_used_at: '2026-03-07T10:02:00Z'
    }

    expect(buildOpenAIUsageRefreshKey(base)).not.toBe(buildOpenAIUsageRefreshKey(next))
  })

  it('非 OpenAI OAuth 账号返回空 key', () => {
    expect(buildOpenAIUsageRefreshKey({
      id: 2,
      platform: 'anthropic',
      type: 'oauth',
      updated_at: '2026-03-07T10:00:00Z',
      last_used_at: '2026-03-07T10:00:00Z',
      extra: {}
    } as any)).toBe('')
  })
})

describe('buildGrokUsageRefreshKey', () => {
  it('会在 last_used_at 或 billing 快照变化时生成不同 key', () => {
    const base = {
      id: 10,
      platform: 'grok',
      type: 'oauth',
      updated_at: '2026-07-12T01:00:00Z',
      last_used_at: '2026-07-12T01:00:00Z',
      extra: {
        grok_billing_snapshot: {
          usage_percent: 33,
          used_cents: 1315,
          monthly_limit_cents: 15000,
          fetched_at: '2026-07-12T01:00:00Z'
        }
      }
    } as any

    expect(buildGrokUsageRefreshKey(base)).not.toBe(
      buildGrokUsageRefreshKey({
        ...base,
        last_used_at: '2026-07-12T01:05:00Z'
      })
    )

    expect(buildGrokUsageRefreshKey(base)).not.toBe(
      buildGrokUsageRefreshKey({
        ...base,
        extra: {
          grok_billing_snapshot: {
            ...base.extra.grok_billing_snapshot,
            usage_percent: 40,
            fetched_at: '2026-07-12T01:06:00Z'
          }
        }
      })
    )
  })

  it('非 Grok OAuth 账号返回空 key', () => {
    expect(
      buildGrokUsageRefreshKey({
        id: 11,
        platform: 'openai',
        type: 'oauth',
        updated_at: '2026-07-12T01:00:00Z',
        last_used_at: '2026-07-12T01:00:00Z',
        extra: {}
      } as any)
    ).toBe('')
  })
})

describe('buildAccountUsageRefreshKey', () => {
  it('OpenAI / Grok 各自有 key，其它平台为空', () => {
    const openai = {
      id: 1,
      platform: 'openai',
      type: 'oauth',
      updated_at: 't',
      last_used_at: 't',
      extra: { codex_5h_used_percent: 1 }
    } as any
    const grok = {
      id: 2,
      platform: 'grok',
      type: 'oauth',
      updated_at: 't',
      last_used_at: 't',
      extra: { grok_billing_snapshot: { usage_percent: 1 } }
    } as any

    expect(buildAccountUsageRefreshKey(openai)).toBe(buildOpenAIUsageRefreshKey(openai))
    expect(buildAccountUsageRefreshKey(grok)).toBe(buildGrokUsageRefreshKey(grok))
    expect(buildAccountUsageRefreshKey({ ...openai, platform: 'anthropic' } as any)).toBe('')
  })
})

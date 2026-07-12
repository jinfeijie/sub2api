import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import GrokQuotaProbeCell from '../GrokQuotaProbeCell.vue'
import type { Account } from '@/types'

const { queryQuota } = vi.hoisted(() => ({ queryQuota: vi.fn() }))

vi.mock('@/api/admin', () => ({
  adminAPI: { grok: { queryQuota } }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const account = {
  id: 1,
  platform: 'grok',
  type: 'oauth'
} as Account

describe('GrokQuotaProbeCell', () => {
  it('uses response reason for empty billing classification', async () => {
    queryQuota.mockRejectedValueOnce({
      status: 502,
      code: 502,
      reason: 'GROK_QUOTA_BILLING_EMPTY',
      message: 'xAI billing endpoints returned no quota data'
    })
    const wrapper = mount(GrokQuotaProbeCell, { props: { account } })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(wrapper.get('[title="admin.accounts.usageWindow.grokProbeErrorEmpty"]')).toBeTruthy()
  })

  it('shows a warning when billing could not be persisted', async () => {
    queryQuota.mockResolvedValueOnce({
      source: 'active_probe',
      billing: { period_type: 'weekly', usage_percent: 10 },
      fetched_at: 1,
      persisted: false
    })
    const wrapper = mount(GrokQuotaProbeCell, { props: { account } })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(wrapper.get('[title="admin.accounts.usageWindow.grokProbePersistWarning"]')).toBeTruthy()
  })
})

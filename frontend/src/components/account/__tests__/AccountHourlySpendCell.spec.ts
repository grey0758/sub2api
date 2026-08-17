import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountHourlySpendCell from '../AccountHourlySpendCell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, values?: Record<string, unknown>) => {
        if (key === 'admin.accounts.hourlySpend.usage') {
          return `${values?.used} / ${values?.limit}`
        }
        if (key === 'admin.accounts.hourlySpend.resetsIn') {
          return `reset:${values?.time}`
        }
        return key
      }
    })
  }
})

const buildAccount = (overrides: Record<string, unknown> = {}) => ({
  id: 1,
  name: 'capped-account',
  platform: 'openai',
  type: 'oauth',
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  schedulable: true,
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-08-17T00:00:00Z',
  updated_at: '2026-08-17T00:00:00Z',
  ...overrides
}) as any

describe('AccountHourlySpendCell', () => {
  it('shows an explicit off state when the account has no hourly limit', () => {
    const wrapper = mount(AccountHourlySpendCell, {
      props: { account: buildAccount({ hourly_spend_limit_enabled: false }) }
    })

    expect(wrapper.text()).toBe('admin.accounts.hourlySpend.off')
  })

  it('shows current spend, the cap state, and the recovery countdown', () => {
    const now = Date.now()
    const wrapper = mount(AccountHourlySpendCell, {
      props: {
        now,
        account: buildAccount({
          hourly_spend_limit_enabled: true,
          hourly_spend_limit_usd: 100,
          hourly_spend_used_usd: 120,
          hourly_spend_window_started_at: new Date(now - 30 * 60_000).toISOString(),
          hourly_spend_window_ends_at: new Date(now + 30 * 60_000).toISOString(),
          hourly_spend_limit_reached: true
        })
      }
    })

    expect(wrapper.get('[data-testid="account-hourly-spend-status"]').text()).toBe(
      'admin.accounts.hourlySpend.capped'
    )
    expect(wrapper.get('[data-testid="account-hourly-spend-usage"]').text()).toContain('$120.00 / $100.00')
    expect(wrapper.get('[data-testid="account-hourly-spend-reset"]').text()).toContain('reset:')
  })

  it('shows the account as ready once the fixed window has expired', () => {
    const now = Date.now()
    const wrapper = mount(AccountHourlySpendCell, {
      props: {
        now,
        account: buildAccount({
          hourly_spend_limit_enabled: true,
          hourly_spend_limit_usd: 100,
          hourly_spend_used_usd: 100,
          hourly_spend_window_started_at: new Date(now - 2 * 60 * 60_000).toISOString(),
          hourly_spend_window_ends_at: new Date(now - 60 * 60_000).toISOString(),
          hourly_spend_limit_reached: true
        })
      }
    })

    expect(wrapper.get('[data-testid="account-hourly-spend-status"]').text()).toBe(
      'admin.accounts.hourlySpend.ready'
    )
    expect(wrapper.get('[data-testid="account-hourly-spend-usage"]').text()).toContain('$0.00 / $100.00')
    expect(wrapper.text()).toContain('admin.accounts.hourlySpend.startsOnUse')
  })
})

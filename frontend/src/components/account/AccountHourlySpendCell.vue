<template>
  <div class="min-w-[8.5rem] text-xs" data-testid="account-hourly-spend">
    <div v-if="!account.hourly_spend_limit_enabled" class="text-gray-400 dark:text-dark-500">
      {{ t('admin.accounts.hourlySpend.off') }}
    </div>
    <div v-else class="space-y-1">
      <div class="flex items-center justify-between gap-2">
        <span
          :class="[
            'inline-flex rounded px-1.5 py-0.5 font-medium',
            limitReached
              ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
              : 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
          ]"
          data-testid="account-hourly-spend-status"
        >
          {{
            t(
              limitReached
                ? 'admin.accounts.hourlySpend.capped'
                : windowActive
                  ? 'admin.accounts.hourlySpend.active'
                  : 'admin.accounts.hourlySpend.ready'
            )
          }}
        </span>
      </div>
      <div class="font-mono text-gray-700 dark:text-gray-300" data-testid="account-hourly-spend-usage">
        {{ t('admin.accounts.hourlySpend.usage', { used: formatCurrency(usedUSD), limit: formatCurrency(limitUSD) }) }}
      </div>
      <div class="h-1.5 overflow-hidden rounded bg-gray-200 dark:bg-dark-600" aria-hidden="true">
        <div
          class="h-full rounded transition-[width]"
          :class="limitReached ? 'bg-red-500' : 'bg-emerald-500'"
          :style="{ width: `${progressPercent}%` }"
        />
      </div>
      <div
        v-if="windowActive && countdown"
        class="text-gray-500 dark:text-gray-400"
        :title="formatDateTime(account.hourly_spend_window_ends_at)"
        data-testid="account-hourly-spend-reset"
      >
        {{ t('admin.accounts.hourlySpend.resetsIn', { time: countdown }) }}
      </div>
      <div v-else class="text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.hourlySpend.startsOnUse') }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { Account } from '@/types'
import { formatCountdown, formatCurrency, formatDateTime } from '@/utils/format'

const props = withDefaults(
  defineProps<{
    account: Account
    now?: number
  }>(),
  {
    now: () => Date.now()
  }
)

const { t } = useI18n()

const windowEndMs = computed(() => {
  if (!props.account.hourly_spend_window_ends_at) return 0
  const value = new Date(props.account.hourly_spend_window_ends_at).getTime()
  return Number.isFinite(value) ? value : 0
})
const windowActive = computed(() => windowEndMs.value > props.now)
const limitUSD = computed(() => Math.max(0, props.account.hourly_spend_limit_usd ?? 0))
const usedUSD = computed(() => windowActive.value ? Math.max(0, props.account.hourly_spend_used_usd ?? 0) : 0)
const limitReached = computed(() =>
  windowActive.value &&
  (props.account.hourly_spend_limit_reached || (limitUSD.value > 0 && usedUSD.value >= limitUSD.value))
)
const progressPercent = computed(() => {
  if (limitUSD.value <= 0) return 0
  return Math.min(100, Math.max(0, (usedUSD.value / limitUSD.value) * 100))
})
const countdown = computed(() =>
  windowActive.value ? formatCountdown(props.account.hourly_spend_window_ends_at) : null
)
</script>

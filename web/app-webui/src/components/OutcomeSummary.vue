<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { Outcome } from '../protocol'
defineProps<{ outcome?: Outcome }>()
const { t } = useI18n()
</script>
<template>
  <div v-if="outcome" class="outcome-summary">
    <div class="metric"><span>{{ t('duration') }}</span><strong>{{ (outcome.durationNs / 1e9).toFixed(1) }}s</strong></div>
    <div class="metric"><span>{{ t('rounds') }}</span><strong>{{ outcome.accounting.rounds }}</strong></div>
    <div class="metric"><span>{{ t('modelInvocations') }}</span><strong>{{ outcome.accounting.modelInvocations }}</strong></div>
    <div class="metric"><span>{{ t('toolCalls') }}</span><strong>{{ outcome.accounting.toolCalls }}</strong></div>
    <template v-if="outcome.accounting.usage.coverage !== 'unavailable'">
      <div class="metric"><span>{{ t('inputTokens') }}</span><strong>{{ outcome.accounting.usage.inputTokens.toLocaleString() }}</strong></div>
      <div class="metric"><span>{{ t('outputTokens') }}</span><strong>{{ outcome.accounting.usage.outputTokens.toLocaleString() }}</strong></div>
    </template>
    <div class="muted col-span-2 text-xs">{{ t('coverage.' + outcome.accounting.usage.coverage) }}</div>
    <p v-if="outcome.failure" class="error-text col-span-2">{{ outcome.failure.stage }}</p>
  </div>
</template>

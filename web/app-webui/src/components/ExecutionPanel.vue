<script setup lang="ts">
import { computed } from 'vue'
import { Activity, ChevronRight } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRuntime } from '../stores/runtime'
import type { TraceEvent } from '../protocol'
import JsonBlock from './JsonBlock.vue'
import OutcomeSummary from './OutcomeSummary.vue'
import StatusBadge from './StatusBadge.vue'
const props = defineProps<{ sessionId: string }>()
const runtime = useRuntime()
const { t } = useI18n()
const turns = computed(() => Object.values(runtime.turns).filter(turn => turn.sessionId === props.sessionId))
const traces = computed(() => {
  const groups = new Map<string, { id: string; events: TraceEvent[]; rounds: Map<number, TraceEvent[]> }>()
  for (const event of runtime.traces[props.sessionId] || []) {
    const id = event.scope?.agent?.turnId
    if (!id) continue
    if (!groups.has(id)) groups.set(id, { id, events: [], rounds: new Map() })
    const group = groups.get(id)!
    const round = event.scope?.agent?.roundIndex
    if (round === undefined) group.events.push(event)
    else { if (!group.rounds.has(round)) group.rounds.set(round, []); group.rounds.get(round)!.push(event) }
  }
  return Array.from(groups.values()).reverse()
})
function eventLabel(event: TraceEvent) {
  return event.data.call?.name || event.data.request?.model || event.data.response?.model || event.type.replace('agent.', '')
}
</script>
<template>
  <div class="execution-panel">
    <p class="muted text-xs leading-relaxed mb-5">{{ t('liveOnly') }}</p>
    <section v-for="turn in turns" :key="turn.id" class="execution-outcome">
      <div class="flex items-center justify-between gap-3 mb-3"><span class="eyebrow">{{ t('turn') }}</span><StatusBadge :status="turn.status" /></div>
      <OutcomeSummary :outcome="turn.outcome" />
      <p v-if="turn.error" class="error-text">{{ turn.error.message }}</p>
    </section>
    <section v-for="group in traces" :key="group.id" class="trace-turn">
      <div class="trace-title"><Activity :size="15" /><span>{{ t('turn') }}</span><code class="muted">{{ group.id.slice(0, 8) }}</code></div>
      <details v-for="[round, events] in group.rounds" :key="round" class="trace-round" open>
        <summary><ChevronRight :size="14" class="disclosure-chevron" />{{ t('round') }} {{ round }}</summary>
        <div class="trace-events">
          <details v-for="event in events" :key="event.cursor" class="trace-event">
            <summary><span class="trace-dot" :class="{ failed: event.data.status === 'failed' }" /><span class="truncate">{{ eventLabel(event) }}</span><span class="event-kind">{{ event.type.split('.').at(-1) }}</span></summary>
            <JsonBlock :value="event.data" />
          </details>
        </div>
      </details>
      <details v-for="event in group.events" :key="event.cursor" class="trace-event">
        <summary>{{ event.type.replace('agent.', '') }}</summary><JsonBlock :value="event.data" />
      </details>
    </section>
    <div v-if="!turns.length && !traces.length" class="empty-panel"><Activity :size="28" /><p>{{ t('noExecution') }}</p></div>
  </div>
</template>

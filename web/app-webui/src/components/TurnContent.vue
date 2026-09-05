<script setup lang="ts">
import { computed } from 'vue'
import { ChevronRight } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Interaction, LiveTurn, Part, TurnBlock } from '../protocol'
import ContentParts from './ContentParts.vue'
import MarkdownContent from './MarkdownContent.vue'
import ToolCard from './ToolCard.vue'
import InteractionCard from './InteractionCard.vue'

const props = defineProps<{ turn: LiveTurn; interactions: Record<string, Interaction>; historicalToolIds: Set<string> }>()
const { t } = useI18n()
type DisplayBlock = TurnBlock | { id: string; kind: 'content'; parts: Part[] }
const blocks = computed<DisplayBlock[]>(() => {
  const items: DisplayBlock[] = [...(props.turn.blocks || [])]
  // Canonical output belongs to the final response, not to earlier commentary.
  if (props.turn.result?.output) {
    const last = items.at(-1)
    const result: DisplayBlock = { id: last?.kind === 'output' ? last.id : 'result', kind: 'content', parts: props.turn.result.output }
    if (last?.kind === 'output') items.splice(-1, 1, result)
    else items.push(result)
  }
  return items.filter(block => block.kind === 'tool' ? !props.historicalToolIds.has(block.call.id)
    : block.kind !== 'interaction' || props.interactions[block.interactionId])
})
</script>
<template>
  <div class="turn-content">
    <template v-for="block in blocks" :key="block.id">
      <details v-if="block.kind === 'reasoning'" class="reasoning-block">
        <summary><ChevronRight :size="14" class="disclosure-chevron" />{{ t('reasoning') }}</summary>
        <MarkdownContent :text="block.text" />
      </details>
      <MarkdownContent v-else-if="block.kind === 'output'" :text="block.text" />
      <ContentParts v-else-if="block.kind === 'content'" :parts="block.parts" />
      <ToolCard v-else-if="block.kind === 'tool'" :name="block.call.name" :arguments="block.call.arguments" :content="block.content" :status="block.status" :error="block.error" />
      <InteractionCard v-else-if="block.kind === 'interaction'" :interaction="interactions[block.interactionId]!" />
    </template>
    <div v-if="turn.status === 'running'" class="thinking-dots" role="status" :aria-label="t('status.running')"><i /><i /><i /></div>
  </div>
</template>

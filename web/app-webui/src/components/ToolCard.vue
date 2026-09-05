<script setup lang="ts">
import { Terminal, ChevronRight } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Interaction, Part } from '../protocol'
import ContentParts from './ContentParts.vue'
import JsonBlock from './JsonBlock.vue'
import InteractionCard from './InteractionCard.vue'
import StatusBadge from './StatusBadge.vue'
defineProps<{ name: string; arguments?: unknown; content?: Part[]; status?: string; error?: string; interactions?: Interaction[] }>()
const { t } = useI18n()
</script>
<template>
  <div class="tool-card">
    <details>
      <summary class="tool-summary"><Terminal :size="15" /><span class="font-mono truncate">{{ name }}</span><StatusBadge v-if="status" :status="status" /><ChevronRight :size="14" class="disclosure-chevron ml-auto shrink-0" /></summary>
      <div class="tool-body">
        <template v-if="arguments !== undefined"><span class="eyebrow">{{ t('arguments') }}</span><JsonBlock :value="arguments" /></template>
        <template v-if="content?.length"><span class="eyebrow">{{ t('result') }}</span><ContentParts :parts="content" /></template>
        <p v-if="error" class="error-text">{{ error }}</p>
      </div>
    </details>
    <InteractionCard v-for="item in interactions" :key="item.id" :interaction="item" />
  </div>
</template>

<script setup lang="ts">
import type { Part } from '../protocol'
import MarkdownContent from './MarkdownContent.vue'
import MediaPart from './MediaPart.vue'
defineProps<{ parts: Part[] }>()
</script>
<template>
  <template v-for="(part, index) in parts" :key="index">
    <MarkdownContent v-if="part.kind === 'text'" :text="part.text || ''" />
    <MediaPart v-else :part="part" />
  </template>
</template>

<script setup lang="ts">
import { Anvil } from 'lucide-vue-next'
defineProps<{ large?: boolean; wordmark?: boolean }>()
</script>

<template>
  <span class="brand" :class="{ large }">
    <span class="brand-mark"><Anvil :size="large ? 28 : 20" :stroke-width="1.65" /><i /></span>
    <span v-if="wordmark" class="brand-name">ingot<span class="accent">.</span></span>
  </span>
</template>

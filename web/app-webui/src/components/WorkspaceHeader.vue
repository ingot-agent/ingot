<script setup lang="ts">
import { Inbox, PanelLeft } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRuntime } from '../stores/runtime'

defineEmits<{ navigation: []; pending: [] }>()
const runtime = useRuntime()
const { t } = useI18n()
</script>
<template>
  <header class="workspace-header">
    <button class="icon-button nav-toggle" :aria-label="t('conversations')" @click="$emit('navigation')"><PanelLeft :size="19" /></button>
    <div class="workspace-header-title"><slot /></div>
    <div class="workspace-header-actions">
      <span class="connection-label"><i class="connection-dot" :class="{ online: runtime.connection === 'online' }" />{{ t('connection.' + runtime.connection) }}</span>
      <button class="icon-button pending-button" :aria-label="t('pending')" @click="$emit('pending')"><Inbox :size="18" /><span v-if="runtime.pendingCount" class="pending-count">{{ runtime.pendingCount }}</span></button>
      <slot name="actions" />
    </div>
  </header>
</template>

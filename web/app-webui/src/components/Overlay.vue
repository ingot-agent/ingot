<script setup lang="ts">
import { DialogRoot, DialogPortal, DialogOverlay, DialogContent, DialogTitle, DialogDescription, DialogClose } from 'reka-ui'
import { X } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
defineProps<{ open: boolean; title: string; description?: string; drawer?: boolean }>()
defineEmits<{ 'update:open': [value: boolean] }>()
const { t } = useI18n()
</script>

<template>
  <DialogRoot :open="open" @update:open="$emit('update:open', $event)">
    <DialogPortal>
      <DialogOverlay class="overlay-scrim" />
      <DialogContent class="overlay-panel" :class="{ drawer }">
        <header class="overlay-heading">
          <div>
            <DialogTitle class="heading">{{ title }}</DialogTitle>
            <DialogDescription :class="description ? 'muted mt-1 text-sm' : 'sr-only'">{{ description || title }}</DialogDescription>
          </div>
          <DialogClose class="icon-button" :aria-label="t('close')"><X :size="18" /></DialogClose>
        </header>
        <div class="overlay-body"><slot /></div>
        <footer v-if="$slots.footer" class="overlay-footer"><slot name="footer" /></footer>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

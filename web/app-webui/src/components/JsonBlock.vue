<script setup lang="ts">
import { computed, ref } from 'vue'
import { Copy, Check } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
const props = defineProps<{ value: unknown }>()
const { t } = useI18n()
const copied = ref(false)
const text = computed(() => typeof props.value === 'string' ? props.value : JSON.stringify(props.value, null, 2))
async function copy() {
  try { await navigator.clipboard.writeText(text.value || ''); copied.value = true }
  catch { copied.value = false }
}
</script>
<template>
  <div class="json-block">
    <button type="button" class="icon-button copy-button" :aria-label="t(copied ? 'copied' : 'copy')" @click="copy"><Check v-if="copied" :size="14" /><Copy v-else :size="14" /></button>
    <pre><code>{{ text }}</code></pre>
  </div>
</template>

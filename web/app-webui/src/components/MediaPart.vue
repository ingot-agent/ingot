<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { File, Download, ExternalLink, Image, LoaderCircle } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Part } from '../protocol'
import { errorMessage, segment } from '../api'
const props = defineProps<{ part: Part }>()
const { t } = useI18n()
const url = ref('')
const loading = ref(false)
const error = ref('')
let controller: AbortController | undefined
const mediaType = computed(() => props.part.mimeType?.split(';')[0].toLowerCase() || '')
const previewable = computed(() =>
  /^(image\/(png|jpeg|gif|webp|avif)|audio\/(mpeg|mp4|ogg|wav|webm|flac)|video\/(mp4|webm|ogg))$/.test(mediaType.value))
const external = computed(() => {
  const uri = props.part.source?.uri
  return uri && /^https?:\/\//i.test(uri) ? uri : ''
})
function clear() {
  controller?.abort()
  controller = undefined
  if (url.value) URL.revokeObjectURL(url.value)
  url.value = ''
  error.value = ''
  loading.value = false
}
async function load() {
  if (url.value) return url.value
  loading.value = true
  error.value = ''
  const pending = new AbortController()
  controller?.abort()
  controller = pending
  try {
    const source = props.part.source
    let bytes: Blob
    if (source?.kind === 'asset' && source.assetId) {
      const response = await fetch('/api/assets/' + segment(source.assetId), { signal: pending.signal })
      if (!response.ok) throw new Error((await response.json()).error?.message || response.statusText)
      bytes = await response.blob()
    } else if (source?.kind === 'inline') {
      const raw = atob(source.data || '')
      bytes = new Blob([Uint8Array.from(raw, character => character.charCodeAt(0))])
    } else throw new Error(t('mediaUnavailable'))
    if (pending.signal.aborted) return ''
    url.value = URL.createObjectURL(new Blob([bytes], { type: previewable.value ? mediaType.value : 'application/octet-stream' }))
    return url.value
  } catch (cause) {
    if (!pending.signal.aborted) error.value = errorMessage(cause)
    return ''
  } finally { if (controller === pending) loading.value = false }
}
async function download() {
  const href = await load()
  if (!href) return
  const link = document.createElement('a')
  link.href = href
  link.download = props.part.name || 'attachment'
  link.click()
}
watch(() => props.part, clear)
onBeforeUnmount(clear)
</script>
<template>
  <div class="media-part">
    <div class="file-row">
      <Image v-if="part.kind === 'image'" :size="19" class="muted shrink-0" /><File v-else :size="19" class="muted shrink-0" />
      <div class="min-w-0 flex-1"><div class="truncate text-sm font-medium">{{ part.name || part.kind }}</div><div class="muted text-xs">{{ part.mimeType }}</div></div>
      <a v-if="external" class="icon-button" :href="external" target="_blank" rel="noopener noreferrer" :aria-label="t('openLink')"><ExternalLink :size="16" /></a>
      <template v-else>
        <button v-if="previewable && !url" class="btn small" :disabled="loading" @click="load">{{ t('preview') }}</button>
        <button class="icon-button" :disabled="loading" :aria-label="t('download')" @click="download"><LoaderCircle v-if="loading" class="spin" :size="16" /><Download v-else :size="16" /></button>
      </template>
    </div>
    <img v-if="url && previewable && part.kind === 'image'" :src="url" :alt="part.name || ''" class="media-preview" />
    <audio v-if="url && previewable && part.kind === 'audio'" :src="url" controls class="w-full" />
    <video v-if="url && previewable && part.kind === 'video'" :src="url" controls class="media-preview" />
    <p v-if="error" class="error-text">{{ error }}</p>
  </div>
</template>

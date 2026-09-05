<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ArrowUp, Paperclip, Square, X, File, LoaderCircle, RotateCcw } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Attachment, LiveTurn } from '../protocol'
import { request, errorMessage } from '../api'
import { useRuntime } from '../stores/runtime'
const props = defineProps<{ sessionKey: string; running: LiveTurn[]; archived?: boolean; sending?: boolean }>()
const emit = defineEmits<{ send: [input: string, attachments: Attachment[], done: () => void] }>()
const runtime = useRuntime()
const { t } = useI18n()
const text = ref('')
const input = ref<HTMLTextAreaElement>()
const picker = ref<HTMLInputElement>()
const dragging = ref(false)
let dragDepth = 0
interface Upload { id: number; file: File; status: 'uploading' | 'ready' | 'failed'; attachment?: Attachment; preview?: string; error?: string; controller?: AbortController }
const uploads = ref<Upload[]>([])
const drafts = new Map<string, { text: string; uploads: Upload[] }>()
let nextId = 0
const canSend = computed(() => runtime.connection === 'online' && !props.sending && !props.archived &&
  !props.running.length && (!!text.value.trim() || uploads.value.length > 0) && uploads.value.every(item => item.status === 'ready'))
function fit() { if (input.value) { input.value.style.height = 'auto'; input.value.style.height = Math.min(input.value.scrollHeight, 200) + 'px' } }
watch(() => props.sessionKey, (key, previous) => {
  drafts.set(previous, { text: text.value, uploads: uploads.value })
  if (previous === 'new' && props.sending && !drafts.has(key)) {
    drafts.set(key, drafts.get(previous)!)
    drafts.delete(previous)
  }
  const draft = drafts.get(key)
  text.value = draft?.text || ''
  uploads.value = draft?.uploads || []
})
watch(text, fit, { flush: 'post' })
const size = (bytes: number) => bytes < 1048576 ? Math.ceil(bytes / 1024) + ' KB' : (bytes / 1048576).toFixed(1) + ' MB'
function release(item: Upload) { item.controller?.abort(); if (item.preview) URL.revokeObjectURL(item.preview) }
function remove(id: number) {
  const item = uploads.value.find(item => item.id === id)
  if (item) release(item)
  uploads.value = uploads.value.filter(item => item.id !== id)
}
async function upload(item: Upload) {
  item.status = 'uploading'
  item.error = ''
  const controller = new AbortController()
  item.controller = controller
  try {
    const result = await request<{ id: string; size: number }>('/assets', { method: 'POST', body: item.file, signal: controller.signal })
    const mimeType = item.file.type || 'application/octet-stream'
    const prefix = mimeType.split('/')[0]
    item.attachment = {
      kind: ['image', 'audio', 'video'].includes(prefix) ? prefix : 'file',
      mimeType, name: item.file.name, assetId: result.id,
    }
    item.status = 'ready'
  } catch (error) {
    if (!controller.signal.aborted) { item.status = 'failed'; item.error = errorMessage(error) }
  }
}
function add(files: FileList | File[]) {
  if (!runtime.assets.available || props.archived || props.sending) return
  for (const file of Array.from(files)) {
    if (file.size > runtime.assets.maxBytes) {
      runtime.notify(file.name + ': ' + t('uploadLimit', { size: size(runtime.assets.maxBytes) }))
      continue
    }
    const item: Upload = {
      id: ++nextId, file, status: 'uploading',
      preview: /^image\/(png|jpeg|gif|webp|avif)$/.test(file.type) ? URL.createObjectURL(file) : undefined,
    }
    uploads.value.push(item)
    // Upload through the reactive object so progress settles in the template.
    void upload(uploads.value[uploads.value.length - 1])
  }
}
function drop(event: DragEvent) { dragging.value = false; dragDepth = 0; if (event.dataTransfer?.files) add(event.dataTransfer.files) }
function paste(event: ClipboardEvent) {
  const files = Array.from(event.clipboardData?.files || []).filter(file => file.type.startsWith('image/'))
  if (files.length && runtime.assets.available) { event.preventDefault(); add(files) }
}
function send() {
  if (!canSend.value) return
  const sentList = uploads.value
  const sentUploads = [...uploads.value]
  emit('send', text.value, sentUploads.flatMap(item => item.attachment ? [item.attachment] : []), () => {
    for (const item of sentUploads) release(item)
    for (const [key, draft] of drafts) if (draft.uploads === sentList) drafts.delete(key)
    if (uploads.value === sentList) { text.value = ''; uploads.value = [] }
  })
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && event.keyCode !== 229) { event.preventDefault(); send() }
}
async function stop() {
  for (const turn of props.running) {
    try { await runtime.stop(turn) } catch (error) { runtime.notify(errorMessage(error)) }
  }
}
onBeforeUnmount(() => {
  for (const draft of drafts.values()) for (const item of draft.uploads) release(item)
  for (const item of uploads.value) release(item)
})
</script>
<template>
  <div class="composer-wrap">
    <form class="composer" :class="{ dragging }" @submit.prevent="send" @dragover.prevent @dragenter.prevent="dragDepth++; dragging = true" @dragleave.prevent="dragDepth--; dragging = dragDepth > 0" @drop.prevent="drop">
      <div v-if="dragging" class="drop-target">{{ t('attachHint') }}</div>
      <div v-if="uploads.length" class="upload-list">
        <div v-for="item in uploads" :key="item.id" class="upload-chip" :class="{ failed: item.status === 'failed' }" :title="item.error">
          <img v-if="item.preview" :src="item.preview" alt="" /><File v-else :size="20" class="muted" />
          <span class="min-w-0"><strong class="block truncate">{{ item.file.name }}</strong><small>{{ item.status === 'ready' ? size(item.file.size) : t(item.status === 'failed' ? 'uploadFailed' : 'uploading') }}</small></span>
          <LoaderCircle v-if="item.status === 'uploading'" class="spin shrink-0" :size="14" />
          <button v-if="item.status === 'failed'" type="button" class="icon-button" :aria-label="t('retry')" @click="upload(item)"><RotateCcw :size="14" /></button>
          <button type="button" class="icon-button" :aria-label="t('remove') + ': ' + item.file.name" :disabled="sending" @click="remove(item.id)"><X :size="13" /></button>
        </div>
      </div>
      <textarea ref="input" v-model="text" class="composer-input" :placeholder="t('composer')" :aria-label="t('composer')" rows="2" :disabled="archived || sending" @keydown="keydown" @paste="paste" />
      <div class="composer-toolbar">
        <input ref="picker" type="file" multiple class="sr-only" tabindex="-1" @change="add(($event.target as HTMLInputElement).files || []); ($event.target as HTMLInputElement).value = ''" />
        <button type="button" class="icon-button" :disabled="!runtime.assets.available || archived || sending" :aria-label="t('attach')" :title="runtime.assets.available ? t('uploadLimit', { size: size(runtime.assets.maxBytes) }) : t('assetUnavailable')" @click="picker?.click()"><Paperclip :size="19" /></button>
        <span class="composer-agent"><span class="tiny-square" />Ingot</span>
        <button v-if="running.length" type="button" class="send-button stopping-button ml-auto" :disabled="running.every(turn => turn.stopping) || runtime.connection !== 'online'" :aria-label="t(running.some(turn => turn.stopping) ? 'stopping' : 'stop')" @click="stop"><LoaderCircle v-if="running.some(turn => turn.stopping)" class="spin" :size="18" /><Square v-else :size="14" fill="currentColor" /></button>
        <button v-else class="send-button ml-auto" type="submit" :disabled="!canSend" :aria-label="t('send')"><LoaderCircle v-if="sending" class="spin" :size="18" /><ArrowUp v-else :size="20" /></button>
      </div>
    </form>
    <div class="composer-hint">{{ t('shortcut') }}</div>
  </div>
</template>

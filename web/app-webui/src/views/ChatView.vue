<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Activity, ArrowDown, ChevronRight, Copy, LoaderCircle, X } from 'lucide-vue-next'
import { useRuntime } from '../stores/runtime'
import { APIError, errorMessage } from '../api'
import type { Attachment, LiveTurn, Message, Part } from '../protocol'
import Brand from '../components/Brand.vue'
import Composer from '../components/Composer.vue'
import ContentParts from '../components/ContentParts.vue'
import TurnContent from '../components/TurnContent.vue'
import ToolCard from '../components/ToolCard.vue'
import InteractionCard from '../components/InteractionCard.vue'
import ExecutionPanel from '../components/ExecutionPanel.vue'
import SessionMenu from '../components/SessionMenu.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Overlay from '../components/Overlay.vue'
import JsonBlock from '../components/JsonBlock.vue'
import WorkspaceHeader from '../components/WorkspaceHeader.vue'
import { readPreference, savePreference } from '../theme'
defineEmits<{ navigation: []; pending: [] }>()
const runtime = useRuntime()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const sessionId = computed(() => typeof route.params.id === 'string' ? route.params.id : '')
const session = computed(() => runtime.sessions.find(item => item.id === sessionId.value))
const running = computed(() => runtime.running(sessionId.value))
const messages = computed(() => runtime.histories[sessionId.value] || [])
const liveTurns = computed(() => Object.values(runtime.turns).filter(turn => turn.sessionId === sessionId.value))
const requests = computed(() => Object.values(runtime.interactions).filter(item => item.scope?.agent?.sessionId === sessionId.value))
const hostStates = computed(() => Object.values(runtime.interactionStates).filter(item => item.scope?.agent?.sessionId === sessionId.value || !item.scope))
const sending = ref(false)
const details = ref(readPreference('details', 'closed') === 'open')
const narrow = ref(window.matchMedia('(max-width: 1199px)').matches)
const scroll = ref<HTMLElement>()
const following = ref(true)
type TranscriptEntry = { id: string; kind: 'message'; message: Message } | { id: string; kind: 'turn'; turn: LiveTurn }
const transcript = computed(() => {
  const entries: TranscriptEntry[] = []
  const settled = liveTurns.value.filter(turn => turn.reconciled)
  const appendTurns = (end: number) => {
    for (const turn of settled.filter(turn => turn.historyEnd === end)) entries.push({ id: turn.id, kind: 'turn', turn })
  }
  appendTurns(0)
  messages.value.forEach((message, index) => {
    if (message.role !== 'tool') entries.push({ id: 'history-' + index, kind: 'message', message })
    appendTurns(index + 1)
  })
  for (const turn of liveTurns.value.filter(turn => !turn.reconciled)) {
    const local = runtime.optimistic[turn.id]
    if (local) entries.push({ id: 'local-' + turn.id, kind: 'message', message: local.message })
    entries.push({ id: turn.id, kind: 'turn', turn })
  }
  return entries
})
const toolResults = computed(() => new Map(messages.value.filter(message => message.role === 'tool').map(message => [message.toolCallId, message])))
const historicalToolIds = computed(() => new Set(messages.value.flatMap(message => (message.toolCalls || []).map(call => call.id))))
const timelineToolIds = computed(() => new Set(liveTurns.value.flatMap(turn => (turn.blocks || []).flatMap(block => block.kind === 'tool' ? [block.call.id] : []))))
const toolCalls = computed(() => {
  const calls = new Map<string, { id: string; name: string; arguments: unknown; content?: Part[]; status: string; error?: string }>()
  for (const event of runtime.traces[sessionId.value] || []) {
    const id = event.scope?.agent?.toolCallId
    if (!id) continue
    if (event.type === 'agent.tool.started') calls.set(id, { ...event.data.call, status: 'running' })
    const call = calls.get(id)
    if (event.type === 'agent.tool.finished' && call) { call.status = event.data.status; call.content = event.data.result?.content; call.error = event.data.error }
    if (event.type === 'agent.tool.progress' && call) call.content = [...(call.content || []), ...(event.data.progress?.content || [])]
  }
  return [...calls.values()].filter(call => !historicalToolIds.value.has(call.id) && !timelineToolIds.value.has(call.id))
})
const showTurn = (turn: LiveTurn) => !turn.reconciled || (turn.status !== 'succeeded' && !!turn.output)
const timelineRequestIds = computed(() => new Set(liveTurns.value.filter(showTurn).flatMap(turn => (turn.blocks || []).flatMap(block => block.kind === 'interaction' ? [block.interactionId] : []))))
const looseRequests = computed(() => requests.value.filter(item => !timelineRequestIds.value.has(item.id)))
const welcome = computed(() => !sessionId.value)
const media = window.matchMedia('(max-width: 1199px)')
function resize() { narrow.value = media.matches }
media.addEventListener('change', resize)
watch(details, value => savePreference('details', value ? 'open' : 'closed'))
watch(sessionId, id => { runtime.activeSession = id; following.value = true; if (id) void runtime.loadHistory(id) }, { immediate: true })
function onScroll() { if (scroll.value) following.value = scroll.value.scrollHeight - scroll.value.scrollTop - scroll.value.clientHeight < 100 }
async function latest() { following.value = true; await nextTick(); scroll.value?.scrollTo({ top: scroll.value.scrollHeight, behavior: 'smooth' }) }
watch(() => [transcript.value.length, ...liveTurns.value.map(turn => turn.output.length + turn.reasoning.length + (turn.blocks?.length || 0)), requests.value.length, toolCalls.value.length], async () => {
  if (following.value) { await nextTick(); if (scroll.value) scroll.value.scrollTop = scroll.value.scrollHeight }
}, { flush: 'post' })
async function send(input: string, attachments: Attachment[], done: () => void) {
  sending.value = true
  try {
    let id = sessionId.value
    if (!id) {
      const item = await runtime.createSession(t('newChat'))
      id = item.id
      await router.push('/sessions/' + encodeURIComponent(id))
    }
    await runtime.send(id, input, attachments)
    done()
    await latest()
  } catch (error) { runtime.notify(error instanceof APIError ? error.message : t('unknownSend') + ' ' + errorMessage(error)) }
  finally { sending.value = false }
}
async function copy(message: Message) {
  try { await navigator.clipboard.writeText(message.content.filter(part => part.kind === 'text').map(part => part.text).join('\n')); runtime.notify(t('copied'), 'info') }
  catch (error) { runtime.notify(errorMessage(error)) }
}
async function restore() {
  try { await runtime.mutateSession(sessionId.value, 'restore') } catch (error) { runtime.notify(errorMessage(error)) }
}
onBeforeUnmount(() => { media.removeEventListener('change', resize); runtime.activeSession = '' })
</script>
<template>
  <div class="chat-layout" :class="{ 'with-details': details && !narrow }">
    <section class="chat-main">
      <WorkspaceHeader class="conversation-header" @navigation="$emit('navigation')" @pending="$emit('pending')">
        <h1 class="truncate" :title="session?.title || t('newChat')">{{ session?.title || t('newChat') }}</h1><span v-if="session?.archivedAt" class="muted text-xs shrink-0">{{ t('archived') }}</span>
        <template #actions><button class="icon-button" :class="{ accent: details }" :aria-label="t('execution')" :aria-expanded="details" @click="details = !details"><Activity :size="18" /></button><SessionMenu v-if="session" :session="session" /></template>
      </WorkspaceHeader>
      <div ref="scroll" class="conversation-scroll" @scroll.passive="onScroll">
        <div v-if="welcome" class="welcome">
          <Brand large />
          <div class="welcome-eyebrow">{{ t('welcomeNote') }}</div>
          <h2>{{ t('welcome') }}</h2>
          <p>{{ t('welcomeSub') }}</p>
        </div>
        <div v-else class="transcript">
          <div v-if="!session && runtime.connection === 'online'" class="empty-panel"><p>{{ t('missingSession') }}</p><RouterLink to="/new" class="btn">{{ t('back') }}</RouterLink></div>
          <div v-if="runtime.historyLoading[sessionId] && !messages.length" class="history-loading"><LoaderCircle class="spin" :size="16" /><div>{{ t('loadingHistory') }}<p v-if="running.length" class="muted text-xs mt-1">{{ t('historyWaiting') }}</p></div></div>
          <div v-if="runtime.historyErrors[sessionId]" class="error-banner"><p>{{ runtime.historyErrors[sessionId] }}</p><button class="text-button" @click="runtime.loadHistory(sessionId)">{{ t('retry') }}</button></div>
          <template v-for="entry in transcript" :key="entry.id">
            <article v-if="entry.kind === 'message'" class="message" :class="'message-' + entry.message.role">
              <div v-if="entry.message.role !== 'user'" class="message-byline"><Brand /><span>{{ entry.message.role === 'assistant' ? t('assistant') : entry.message.role }}</span></div>
              <div class="message-content"><ContentParts :parts="entry.message.content" /></div>
              <ToolCard v-for="call in entry.message.toolCalls" :key="call.id" :name="call.name" :arguments="call.arguments" :content="toolResults.get(call.id)?.content" />
              <button v-if="entry.message.role === 'assistant' && entry.message.content.length" class="icon-button message-copy" :aria-label="t('copy')" @click="copy(entry.message)"><Copy :size="14" /></button>
            </article>
            <article v-else class="message message-assistant live-message">
              <template v-if="showTurn(entry.turn)">
                <div class="message-byline"><Brand /><span>Ingot</span><StatusBadge :status="entry.turn.status" /></div>
                <TurnContent :turn="entry.turn" :interactions="runtime.interactions" :historical-tool-ids="historicalToolIds" />
              </template>
              <p v-if="entry.turn.error" class="error-banner">{{ entry.turn.error.message }}</p>
              <button v-if="entry.turn.status !== 'running'" class="execution-link" @click="details = true"><Activity :size="13" /><span>{{ t('status.' + entry.turn.status) }}</span><template v-if="entry.turn.outcome"><span>·</span><span>{{ (entry.turn.outcome.durationNs / 1e9).toFixed(1) }}s</span></template><ChevronRight :size="12" /></button>
            </article>
          </template>
          <ToolCard v-for="call in toolCalls" :key="call.id" :name="call.name" :arguments="call.arguments" :content="call.content" :status="call.status" :error="call.error" />
          <InteractionCard v-for="item in looseRequests" :key="item.id" :interaction="item" />
          <details v-for="state in hostStates" :key="state.id" class="host-state"><summary>{{ state.description || state.name }}</summary><JsonBlock :value="state.values" /></details>
        </div>
      </div>
      <div class="composer-dock" :class="{ 'welcome-composer': welcome }">
        <button v-if="!following && !welcome" class="latest-button" @click="latest"><ArrowDown :size="14" />{{ t('showLatest') }}</button>
        <div v-if="session?.archivedAt" class="archive-banner"><span>{{ t('archivedSession') }}</span><button class="btn small" @click="restore">{{ t('restore') }}</button></div>
        <Composer :session-key="sessionId || 'new'" :running="running" :archived="!!session?.archivedAt || (!!sessionId && !session)" :sending="sending" @send="send" />
      </div>
    </section>
    <aside v-if="details && !narrow" class="details-sidebar"><header><h2>{{ t('execution') }}</h2><button class="icon-button" :aria-label="t('close')" @click="details = false"><X :size="17" /></button></header><ExecutionPanel :session-id="sessionId" /></aside>
    <Overlay :open="details && narrow" :title="t('execution')" drawer @update:open="details = $event"><ExecutionPanel :session-id="sessionId" /></Overlay>
  </div>
</template>

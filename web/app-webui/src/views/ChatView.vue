<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Activity, ArrowDown, ChevronRight, Copy, LoaderCircle, X } from 'lucide-vue-next'
import { useRuntime } from '../stores/runtime'
import { APIError, errorMessage } from '../api'
import type { Attachment, Interaction, Message, Part } from '../protocol'
import Brand from '../components/Brand.vue'
import Composer from '../components/Composer.vue'
import ContentParts from '../components/ContentParts.vue'
import MarkdownContent from '../components/MarkdownContent.vue'
import ToolCard from '../components/ToolCard.vue'
import InteractionCard from '../components/InteractionCard.vue'
import ExecutionPanel from '../components/ExecutionPanel.vue'
import SessionMenu from '../components/SessionMenu.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Overlay from '../components/Overlay.vue'
import JsonBlock from '../components/JsonBlock.vue'
import { readPreference, savePreference } from '../theme'
const runtime = useRuntime()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const sessionId = computed(() => typeof route.params.id === 'string' ? route.params.id : '')
const session = computed(() => runtime.sessions.find(item => item.id === sessionId.value))
const running = computed(() => runtime.running(sessionId.value))
const messages = computed(() => runtime.histories[sessionId.value] || [])
const localMessages = computed(() => Object.values(runtime.optimistic).filter(item => item.sessionId === sessionId.value))
const liveTurns = computed(() => Object.values(runtime.turns).filter(turn => turn.sessionId === sessionId.value))
const requests = computed(() => Object.values(runtime.interactions).filter(item => item.scope?.agent?.sessionId === sessionId.value))
const hostStates = computed(() => Object.values(runtime.interactionStates).filter(item => item.scope?.agent?.sessionId === sessionId.value || !item.scope))
const sending = ref(false)
const details = ref(readPreference('details', 'closed') === 'open')
const narrow = ref(window.matchMedia('(max-width: 1199px)').matches)
const scroll = ref<HTMLElement>()
const following = ref(true)
const visibleMessages = computed(() => messages.value.filter(message => message.role !== 'tool'))
const toolResults = computed(() => new Map(messages.value.filter(message => message.role === 'tool').map(message => [message.toolCallId, message])))
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
  const historicalIds = new Set(messages.value.flatMap(message => (message.toolCalls || []).map(call => call.id)))
  return [...calls.values()].filter(call => !historicalIds.has(call.id))
})
const renderedToolIds = computed(() => new Set([...messages.value.flatMap(message => (message.toolCalls || []).map(call => call.id)), ...toolCalls.value.map(call => call.id)]))
const looseRequests = computed(() => requests.value.filter(item => !item.scope?.agent?.toolCallId || !renderedToolIds.value.has(item.scope.agent.toolCallId)))
const toolRequests = (id: string): Interaction[] => requests.value.filter(item => item.scope?.agent?.toolCallId === id)
const welcome = computed(() => !sessionId.value)
const media = window.matchMedia('(max-width: 1199px)')
function resize() { narrow.value = media.matches }
media.addEventListener('change', resize)
watch(details, value => savePreference('details', value ? 'open' : 'closed'))
watch(sessionId, id => { runtime.activeSession = id; following.value = true; if (id) void runtime.loadHistory(id) }, { immediate: true })
function onScroll() { if (scroll.value) following.value = scroll.value.scrollHeight - scroll.value.scrollTop - scroll.value.clientHeight < 100 }
async function latest() { following.value = true; await nextTick(); scroll.value?.scrollTo({ top: scroll.value.scrollHeight, behavior: 'smooth' }) }
watch(() => [messages.value.length, localMessages.value.length, ...liveTurns.value.map(turn => turn.output.length + turn.reasoning.length), requests.value.length, toolCalls.value.length], async () => {
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
      <header class="conversation-header">
        <div class="min-w-0"><h1 class="truncate">{{ session?.title || t('newChat') }}</h1><span v-if="session?.archivedAt" class="muted text-xs">{{ t('archived') }}</span></div>
        <div class="flex items-center gap-1"><button class="icon-button" :class="{ accent: details }" :aria-label="t('execution')" :aria-expanded="details" @click="details = !details"><Activity :size="18" /></button><SessionMenu v-if="session" :session="session" /></div>
      </header>
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
          <article v-for="(message, index) in visibleMessages" :key="index" class="message" :class="'message-' + message.role">
            <div v-if="message.role !== 'user'" class="message-byline"><Brand /><span>{{ message.role === 'assistant' ? t('assistant') : message.role }}</span></div>
            <div class="message-content"><ContentParts :parts="message.content" /></div>
            <ToolCard v-for="call in message.toolCalls" :key="call.id" :name="call.name" :arguments="call.arguments" :content="toolResults.get(call.id)?.content" :interactions="toolRequests(call.id)" />
            <button v-if="message.role === 'assistant' && message.content.length" class="icon-button message-copy" :aria-label="t('copy')" @click="copy(message)"><Copy :size="14" /></button>
          </article>
          <article v-for="(item, index) in localMessages" :key="'local-' + index" class="message message-user"><div class="message-content"><ContentParts :parts="item.message.content" /></div></article>
          <ToolCard v-for="call in toolCalls" :key="call.id" :name="call.name" :arguments="call.arguments" :content="call.content" :status="call.status" :error="call.error" :interactions="toolRequests(call.id)" />
          <article v-for="turn in liveTurns" :key="turn.id" class="message message-assistant live-message">
            <template v-if="!turn.reconciled || (turn.status !== 'succeeded' && turn.output)">
              <div class="message-byline"><Brand /><span>Ingot</span><StatusBadge :status="turn.status" /></div>
              <details v-if="turn.reasoning" class="reasoning-block"><summary><ChevronRight :size="14" class="disclosure-chevron" />{{ t('reasoning') }}</summary><MarkdownContent :text="turn.reasoning" /></details>
              <ContentParts v-if="turn.result?.output" :parts="turn.result.output" />
              <MarkdownContent v-else-if="turn.output" :text="turn.output" />
              <div v-else-if="turn.status === 'running'" class="thinking-dots" role="status" :aria-label="t('status.running')"><i /><i /><i /></div>
            </template>
            <p v-if="turn.error" class="error-banner">{{ turn.error.message }}</p>
            <button v-if="turn.status !== 'running'" class="execution-link" @click="details = true"><Activity :size="13" /><span>{{ t('status.' + turn.status) }}</span><template v-if="turn.outcome"><span>·</span><span>{{ (turn.outcome.durationNs / 1e9).toFixed(1) }}s</span></template><ChevronRight :size="12" /></button>
          </article>
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

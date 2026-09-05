<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { PanelLeft, Inbox, X, ArrowUpRight, Sun, Moon, Monitor } from 'lucide-vue-next'
import { useRuntime } from './stores/runtime'
import { readPreference, savePreference, applyTheme } from './theme'
import Navigation from './components/Navigation.vue'
import Overlay from './components/Overlay.vue'
import InteractionCard from './components/InteractionCard.vue'
const runtime = useRuntime()
const router = useRouter()
const { t, locale } = useI18n()
const collapsed = ref(readPreference('sidebar', 'open') === 'closed')
const mobileNavigation = ref(false)
const settings = ref(false)
const pending = ref(false)
const theme = ref(readPreference('theme', 'light'))
watch(theme, value => { savePreference('theme', value); applyTheme(value) })
watch(locale, value => { savePreference('language', value); document.documentElement.lang = value === 'zh' ? 'zh-CN' : 'en' }, { immediate: true })
watch(collapsed, value => savePreference('sidebar', value ? 'closed' : 'open'))
function keyboard(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.altKey && event.key.toLowerCase() === 'n') {
    event.preventDefault()
    void router.push('/new')
  }
}
function showNavigation() {
  if (window.matchMedia('(max-width: 767px)').matches) mobileNavigation.value = true
  else collapsed.value = !collapsed.value
}
function goRequest(sessionId?: string, operationId?: string) {
  pending.value = false
  void router.push(sessionId ? '/sessions/' + encodeURIComponent(sessionId) : { path: '/operations', query: { invocation: operationId } })
}
onMounted(() => { void runtime.connect(); window.addEventListener('keydown', keyboard) })
onBeforeUnmount(() => { runtime.disconnect(); window.removeEventListener('keydown', keyboard) })
</script>
<template>
  <div class="app-shell" :class="{ 'nav-collapsed': collapsed }">
    <aside class="desktop-navigation"><Navigation @navigate="mobileNavigation = false" @settings="settings = true" @collapse="collapsed = true" /></aside>
    <main class="workspace">
      <div class="workspace-utilities">
        <button class="icon-button nav-toggle" :aria-label="t('conversations')" @click="showNavigation"><PanelLeft :size="19" /></button>
        <div class="ml-auto flex items-center gap-3">
          <span class="connection-label"><i class="connection-dot" :class="{ online: runtime.connection === 'online' }" />{{ t('connection.' + runtime.connection) }}</span>
          <button class="icon-button pending-button" :aria-label="t('pending')" @click="pending = true"><Inbox :size="18" /><span v-if="runtime.pendingCount" class="pending-count">{{ runtime.pendingCount }}</span></button>
        </div>
      </div>
      <div v-if="runtime.connection === 'reconnecting'" class="connection-banner" role="status" :title="runtime.connectionError">{{ t('disconnected') }}</div>
      <RouterView />
    </main>
    <Overlay :open="mobileNavigation" :title="t('conversations')" drawer @update:open="mobileNavigation = $event">
      <Navigation @navigate="mobileNavigation = false" @settings="mobileNavigation = false; settings = true" @collapse="mobileNavigation = false" />
    </Overlay>
    <Overlay :open="settings" :title="t('settings')" :description="t('appearanceDescription')" @update:open="settings = $event">
      <div class="settings-section"><h3 class="field-label">{{ t('theme') }}</h3><div class="theme-options">
        <button v-for="option in [{ value: 'light', icon: Sun }, { value: 'dark', icon: Moon }, { value: 'system', icon: Monitor }]" :key="option.value" class="theme-option" :class="{ selected: theme === option.value }" :aria-pressed="theme === option.value" @click="theme = option.value"><component :is="option.icon" :size="22" /><span>{{ t(option.value) }}</span></button>
      </div></div>
      <div class="settings-section"><label class="field-label" for="language">{{ t('language') }}</label><select id="language" v-model="locale" class="field mt-3"><option value="en">English</option><option value="zh">简体中文</option></select></div>
    </Overlay>
    <Overlay :open="pending" :title="t('pending')" drawer @update:open="pending = $event">
      <div v-if="!runtime.pendingCount" class="empty-panel"><Inbox :size="28" /><p>{{ t('noPending') }}</p></div>
      <div v-for="item in runtime.interactions" :key="item.id" class="mb-5">
        <button v-if="item.scope?.agent?.sessionId || item.scope?.operation" class="text-button mb-2" @click="goRequest(item.scope?.agent?.sessionId, item.scope?.operation?.invocationId)">{{ t(item.scope?.operation ? 'operations' : 'viewConversation') }}<ArrowUpRight :size="14" /></button>
        <InteractionCard :interaction="item" />
      </div>
    </Overlay>
    <div class="toast-list" aria-live="polite">
      <div v-for="notice in runtime.notices.slice(-3)" :key="notice.id" class="toast" :class="{ 'error-toast': notice.level === 'error' }">
        <p>{{ notice.message }}</p><button class="icon-button" :aria-label="t('close')" @click="runtime.notices = runtime.notices.filter(item => item.id !== notice.id)"><X :size="15" /></button>
      </div>
    </div>
  </div>
</template>

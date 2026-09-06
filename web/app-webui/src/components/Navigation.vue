<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Search, Plus, Archive, Layers, Settings2, MessageSquare, Folder, PanelLeftClose } from 'lucide-vue-next'
import { useRuntime } from '../stores/runtime'
import { useRelativeTime, workspaceBasename } from '../time'
import Brand from './Brand.vue'
import SessionMenu from './SessionMenu.vue'
import type { Session } from '../protocol'
defineEmits<{ navigate: []; settings: []; collapse: [] }>()
const { t } = useI18n()
const runtime = useRuntime()
const relativeTime = useRelativeTime()
const search = ref('')
const archived = ref(false)
const sessions = computed(() => runtime.orderedSessions.filter(session =>
  Boolean(session.archivedAt) === archived.value &&
  session.title.toLocaleLowerCase().includes(search.value.toLocaleLowerCase()) ||
  (session.workspace || '').toLocaleLowerCase().includes(search.value.toLocaleLowerCase())))
const hasPending = (id: string) => Object.values(runtime.interactions).some(item => item.scope?.agent?.sessionId === id)

// Group sessions by workspace root, preserving most-recent session order.
// Sessions without a binding collapse under a single "no workspace" group.
interface Group { key: string; label: string; sessions: Session[] }
const groups = computed<Group[]>(() => {
  const ordered: Group[] = []
  const byKey = new Map<string, Group>()
  for (const session of sessions.value) {
    const key = session.workspace || ''
    const label = key ? workspaceBasename(key) : t('unnamedWorkspace')
    let group = byKey.get(key)
    if (!group) {
      group = { key, label, sessions: [] }
      byKey.set(key, group)
      ordered.push(group)
    }
    group.sessions.push(session)
  }
  return ordered
})
</script>
<template>
  <nav class="navigation" :aria-label="t('conversations')">
    <div class="nav-brand"><Brand wordmark /><button class="icon-button" :aria-label="t('close')" @click="$emit('collapse')"><PanelLeftClose :size="17" /></button></div>
    <div class="nav-actions">
      <RouterLink to="/new" class="btn new-conversation" @click="$emit('navigate')"><Plus :size="17" />{{ t('newChat') }}<span class="ml-auto muted text-xs">⌘ ⌥ N</span></RouterLink>
      <div class="search-field"><Search :size="15" /><input v-model="search" :placeholder="t('search')" :aria-label="t('search')" /></div>
    </div>
    <div class="nav-section"><span>{{ t(archived ? 'archived' : 'conversations') }}</span><button class="icon-button" :class="{ 'accent': archived }" :aria-label="t(archived ? 'active' : 'archived')" :aria-pressed="archived" @click="archived = !archived"><Archive :size="14" /></button></div>
    <div class="session-list">
      <template v-for="group in groups" :key="group.key">
        <div class="workspace-group-heading" :title="group.key || t('unnamedWorkspace')">
          <Folder :size="15" /><span class="truncate">{{ group.label }}</span>
        </div>
        <div v-for="session in group.sessions" :key="session.id" class="session-row">
          <RouterLink :to="'/sessions/' + encodeURIComponent(session.id)" class="session-link" @click="$emit('navigate')">
            <span v-if="runtime.running(session.id).length" class="activity-dot" />
            <span v-else-if="hasPending(session.id)" class="pending-dot" />
            <MessageSquare v-else :size="15" class="session-icon" />
            <span class="truncate">{{ session.title || t('newChat') }}</span>
            <time class="session-time" :datetime="session.updatedAt" :title="session.updatedAt">{{ relativeTime(session.updatedAt) }}</time>
          </RouterLink>
          <SessionMenu :session="session" />
        </div>
      </template>
      <p v-if="!groups.length" class="nav-empty">{{ t(search ? 'noMatches' : 'emptySessions') }}</p>
    </div>
    <div class="nav-footer">
      <RouterLink to="/operations" class="nav-bottom-link" @click="$emit('navigate')"><Layers :size="17" />{{ t('operations') }}<span v-if="runtime.operations.length" class="count ml-auto">{{ runtime.operations.length }}</span></RouterLink>
      <button class="nav-bottom-link w-full" @click="$emit('settings')"><Settings2 :size="17" />{{ t('settings') }}</button>
      <div class="workspace-label"><span class="workspace-avatar">i</span><div><span class="text-xs font-medium">{{ t('local') }}</span><div class="muted text-[11px]">Ingot</div></div><span class="connection-dot ml-auto" :class="{ online: runtime.connection === 'online' }" :title="t('connection.' + runtime.connection)" /></div>
    </div>
  </nav>
</template>

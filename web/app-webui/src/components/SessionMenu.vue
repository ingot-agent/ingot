<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { DropdownMenuRoot, DropdownMenuTrigger, DropdownMenuPortal, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator } from 'reka-ui'
import { MoreHorizontal, Pencil, Archive, ArchiveRestore, GitBranch, Trash2 } from 'lucide-vue-next'
import type { Session } from '../protocol'
import { errorMessage } from '../api'
import { useRuntime } from '../stores/runtime'
import Overlay from './Overlay.vue'
const props = defineProps<{ session: Session }>()
const { t } = useI18n()
const runtime = useRuntime()
const router = useRouter()
const action = ref<'rename' | 'delete' | 'fork' | ''>('')
const title = ref('')
const busy = ref(false)
const error = ref('')
const running = computed(() => runtime.running(props.session.id).length > 0)
function open(value: typeof action.value) { action.value = value; title.value = props.session.title; error.value = '' }
async function perform(value: 'rename' | 'archive' | 'restore' | 'delete' | 'fork') {
  busy.value = true
  error.value = ''
  try {
    const item = await runtime.mutateSession(props.session.id, value, title.value)
    action.value = ''
    if (value === 'fork' && item) await router.push('/sessions/' + encodeURIComponent(item.id))
    if (value === 'delete' && router.currentRoute.value.params.id === props.session.id) await router.push('/new')
  } catch (cause) {
    error.value = errorMessage(cause)
    if (!action.value) runtime.notify(error.value)
  } finally { busy.value = false }
}
</script>
<template>
  <DropdownMenuRoot>
    <DropdownMenuTrigger class="icon-button session-menu-trigger" :aria-label="t('details') + ': ' + session.title" @click.stop><MoreHorizontal :size="17" /></DropdownMenuTrigger>
    <DropdownMenuPortal>
      <DropdownMenuContent class="dropdown-menu" align="end" :side-offset="6">
        <DropdownMenuItem class="menu-item" @select="open('rename')"><Pencil :size="15" />{{ t('rename') }}</DropdownMenuItem>
        <DropdownMenuItem class="menu-item" :disabled="running" @select="open('fork')"><GitBranch :size="15" />{{ t('fork') }}</DropdownMenuItem>
        <DropdownMenuItem class="menu-item" :disabled="running || busy" @select="perform(session.archivedAt ? 'restore' : 'archive')"><ArchiveRestore v-if="session.archivedAt" :size="15" /><Archive v-else :size="15" />{{ t(session.archivedAt ? 'restore' : 'archive') }}</DropdownMenuItem>
        <DropdownMenuSeparator class="menu-separator" />
        <DropdownMenuItem class="menu-item danger-text" :disabled="running" @select="open('delete')"><Trash2 :size="15" />{{ t('delete') }}</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
  <Overlay :open="!!action" :title="t(action === 'delete' ? 'deleteTitle' : action === 'fork' ? 'forkTitle' : 'renameTitle')" :description="action === 'delete' ? t('deleteDescription') : action === 'fork' ? t('forkDescription') : undefined" @update:open="!$event && (action = '')">
    <form v-if="action !== 'delete'" id="session-action" @submit.prevent="action && perform(action)">
      <label class="field-label" for="session-title">{{ t('title') }}</label>
      <input id="session-title" v-model="title" class="field mt-2" maxlength="200" required />
    </form>
    <p v-if="error" class="error-text" role="alert">{{ error }}</p>
    <template #footer>
      <button class="btn" :disabled="busy" @click="action = ''">{{ t('cancel') }}</button>
      <button v-if="action === 'delete'" class="btn danger" :disabled="busy" @click="perform('delete')">{{ t('delete') }}</button>
      <button v-else class="btn primary" form="session-action" type="submit" :disabled="busy || !title.trim()">{{ t('save') }}</button>
    </template>
  </Overlay>
</template>

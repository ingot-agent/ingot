<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowUp, Folder, FolderOpen, HardDrive, LoaderCircle } from 'lucide-vue-next'
import { errorMessage, request } from '../api'
import Overlay from './Overlay.vue'

interface BrowseEntry { name: string; path: string }
interface BrowseResult { path: string; parent?: string; roots?: BrowseEntry[]; directories: BrowseEntry[] }

const props = defineProps<{ open: boolean; initialPath?: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; select: [path: string] }>()
const { t } = useI18n()
const current = ref('')
const parent = ref('')
const roots = ref<BrowseEntry[]>([])
const directories = ref<BrowseEntry[]>([])
const loading = ref(false)
const error = ref('')

async function load(path?: string) {
  loading.value = true
  error.value = ''
  try {
    const query = path ? '?path=' + encodeURIComponent(path) : ''
    const result = await request<BrowseResult>('/workspace/browse' + query)
    current.value = result.path
    parent.value = result.parent || ''
    roots.value = result.roots || []
    directories.value = result.directories || []
  } catch (cause) { error.value = errorMessage(cause) } finally { loading.value = false }
}
function enter(path: string) { void load(path) }
function up() { if (parent.value) void load(parent.value) }
function choose() { if (current.value) emit('select', current.value) }

watch(() => props.open, open => { if (open) void load(props.initialPath || undefined) })
</script>

<template>
  <Overlay :open="open" :title="t('workspace')" :description="t('workspacePickDescription')" @update:open="$emit('update:open', $event)">
    <div class="directory-picker">
      <div class="directory-picker-path" :title="current">
        <FolderOpen class="muted shrink-0" :size="15" />
        <span class="truncate">{{ current }}</span>
      </div>
      <div v-if="loading" class="directory-picker-empty"><LoaderCircle class="spin" :size="20" /></div>
      <p v-else-if="error" class="error-banner">{{ error }}</p>
      <template v-else>
        <ul class="directory-picker-list">
          <li v-if="parent">
            <button type="button" class="directory-picker-row" @click="up">
              <ArrowUp :size="16" class="shrink-0" />
              <span>..</span>
            </button>
          </li>
          <li v-for="root in roots" :key="root.path">
            <button type="button" class="directory-picker-row" @click="enter(root.path)">
              <HardDrive :size="16" class="shrink-0" />
              <span class="truncate">{{ root.name }}</span>
            </button>
          </li>
          <li v-for="entry in directories" :key="entry.path">
            <button type="button" class="directory-picker-row" @click="enter(entry.path)">
              <Folder :size="16" class="shrink-0" />
              <span class="truncate">{{ entry.name }}</span>
            </button>
          </li>
        </ul>
        <p v-if="!roots.length && !directories.length" class="directory-picker-empty muted text-sm">{{ t('workspaceNoSubdirectories') }}</p>
      </template>
    </div>
    <template #footer>
      <button class="btn" type="button" @click="$emit('update:open', false)">{{ t('cancel') }}</button>
      <button class="btn primary" type="button" :disabled="loading || !current" @click="choose">{{ t('workspaceSelect') }}</button>
    </template>
  </Overlay>
</template>

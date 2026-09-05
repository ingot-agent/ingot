<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Layers, Play, Square, ArrowUpRight, LoaderCircle, ChevronRight } from 'lucide-vue-next'
import { useRuntime } from '../stores/runtime'
import { errorMessage } from '../api'
import { canRoundtripForm, interactionValues, parseObject, schemaFields, supportsForm } from '../forms'
import FieldControl from '../components/FieldControl.vue'
import InteractionCard from '../components/InteractionCard.vue'
import JsonBlock from '../components/JsonBlock.vue'
import StatusBadge from '../components/StatusBadge.vue'
import WorkspaceHeader from '../components/WorkspaceHeader.vue'
defineEmits<{ navigation: []; pending: [] }>()
const runtime = useRuntime()
const route = useRoute()
const { t } = useI18n()
const name = ref('')
const sessionId = ref('')
const mode = ref<'form' | 'json'>('form')
const json = ref('{}')
const values = ref<Record<string, unknown>>({})
const busy = ref(false)
const error = ref('')
const selected = computed(() => runtime.operations.find(item => item.name === name.value))
const formSupported = computed(() => selected.value ? supportsForm(selected.value.inputSchema) : false)
const fields = computed(() => selected.value ? schemaFields(selected.value.inputSchema) : [])
const calls = computed(() => Object.values(runtime.operationInvocations).slice().reverse())
const canceling = ref<string[]>([])
watch(() => runtime.operations, items => { if (!name.value && items.length) name.value = items[0].name }, { immediate: true })
watch(name, () => {
  values.value = {}
  json.value = '{}'
  mode.value = formSupported.value ? 'form' : 'json'
  error.value = ''
}, { immediate: true })
watch(() => route.query.invocation, id => {
  if (typeof id === 'string') {
    const invocation = runtime.operationInvocations[id]
    if (invocation) name.value = invocation.name
  }
}, { immediate: true })
function setMode(next: 'form' | 'json') {
  error.value = ''
  if (next === mode.value || !selected.value) return
  if (next === 'json') {
    try { json.value = JSON.stringify(interactionValues(fields.value.map(field => ({ ...field, required: false })), values.value), null, 2) }
    catch (cause) { error.value = t(errorMessage(cause)); return }
  } else {
    if (!canRoundtripForm(json.value, selected.value.inputSchema)) { error.value = t('unsafeForm'); return }
    values.value = parseObject(json.value)
  }
  mode.value = next
}
async function run() {
  error.value = ''
  let input: string
  try {
    if (mode.value === 'form') input = JSON.stringify(interactionValues(fields.value, values.value))
    else { parseObject(json.value); input = json.value }
  } catch (cause) { error.value = t(mode.value === 'json' ? 'jsonObject' : errorMessage(cause)); return }
  busy.value = true
  try { await runtime.invoke(name.value, input, sessionId.value) }
  catch (cause) { error.value = errorMessage(cause) }
  finally { busy.value = false }
}
async function stop(id: string) {
  canceling.value.push(id)
  try { await runtime.cancelOperation(id) } catch (cause) { runtime.notify(errorMessage(cause)); canceling.value = canceling.value.filter(item => item !== id) }
}
</script>
<template>
  <WorkspaceHeader @navigation="$emit('navigation')" @pending="$emit('pending')" />
  <div class="operations-view">
    <header class="page-heading"><div class="page-icon"><Layers :size="21" /></div><h1>{{ t('operations') }}</h1><p>{{ t('operationsSub') }}</p></header>
    <div v-if="!runtime.operations.length" class="operations-empty"><Layers :size="36" :stroke-width="1" /><h2>{{ t('noOperations') }}</h2><p>{{ t('noOperationsSub') }}</p></div>
    <div v-else class="operations-grid">
      <section class="operation-form card">
        <label for="operation-name" class="field-label">{{ t('selectOperation') }}</label>
        <select id="operation-name" v-model="name" class="field mt-2"><option v-for="item in runtime.operations" :key="item.name" :value="item.name">{{ item.name }}</option></select>
        <template v-if="selected">
          <p class="muted my-5 text-sm leading-relaxed">{{ selected.description }}</p>
          <form @submit.prevent="run">
            <div class="flex items-center justify-between mb-4"><h2 class="field-label">{{ t('input') }}</h2><div v-if="formSupported" class="segmented-control"><button type="button" :aria-pressed="mode === 'form'" :class="{ selected: mode === 'form' }" @click="setMode('form')">{{ t('form') }}</button><button type="button" :aria-pressed="mode === 'json'" :class="{ selected: mode === 'json' }" @click="setMode('json')">{{ t('json') }}</button></div></div>
            <template v-if="mode === 'form' && formSupported">
              <template v-for="field in fields" :key="field.name">
                <div v-if="selected.inputSchema.properties?.[field.name].enum" class="form-field">
                  <label class="field-label" :for="'op-' + field.name">{{ field.label }}<span v-if="field.required" class="accent"> *</span></label>
                  <select :id="'op-' + field.name" v-model="values[field.name]" class="field">
                    <option :value="undefined">{{ t('choose') }}</option><option v-for="(value, index) in selected.inputSchema.properties[field.name].enum" :key="index" :value="value">{{ String(value) }}</option>
                  </select>
                </div>
                <FieldControl v-else :id="'op-' + field.name" v-model="values[field.name]" :field="field" :disabled="busy" />
              </template>
            </template>
            <template v-else><p v-if="!formSupported" class="muted text-xs mb-3">{{ t('complexSchema') }}</p><textarea v-model="json" class="field json-input" rows="10" :aria-label="t('input')" spellcheck="false" /></template>
            <details class="schema-disclosure"><summary><ChevronRight :size="13" class="disclosure-chevron" />{{ t('schema') }}</summary><JsonBlock :value="selected.inputSchema" /></details>
            <label for="operation-session" class="field-label">{{ t('session') }} <span class="muted font-normal">· {{ t('optional') }}</span></label>
            <select id="operation-session" v-model="sessionId" class="field mt-2 mb-5"><option value="">{{ t('noSession') }}</option><option v-for="session in runtime.orderedSessions" :key="session.id" :value="session.id">{{ session.title }}</option></select>
            <p v-if="error" class="error-text mb-4" role="alert">{{ error }}</p>
            <button class="btn primary w-full" type="submit" :disabled="busy || runtime.connection !== 'online'"><LoaderCircle v-if="busy" :size="15" class="spin" /><Play v-else :size="15" />{{ t('runOperation') }}</button>
          </form>
        </template>
      </section>
      <section class="operation-calls">
        <div class="section-heading"><h2>{{ t('calls') }}</h2><span class="count">{{ calls.length }}</span></div>
        <p class="muted text-xs mb-5">{{ t('retention') }}</p>
        <div v-if="!calls.length" class="empty-panel">{{ t('noCalls') }}</div>
        <article v-for="call in calls" :id="'invocation-' + call.id" :key="call.id" class="card operation-call" :class="{ highlighted: route.query.invocation === call.id }">
          <header><span class="font-mono text-sm truncate">{{ call.name }}</span><StatusBadge :status="call.status" /></header>
          <RouterLink v-if="call.sessionId" :to="'/sessions/' + encodeURIComponent(call.sessionId)" class="text-button mt-2">{{ runtime.sessions.find(item => item.id === call.sessionId)?.title || t('session') }}<ArrowUpRight :size="13" /></RouterLink>
          <button v-if="call.status === 'running'" class="btn small mt-4" :disabled="canceling.includes(call.id) || runtime.connection !== 'online'" @click="stop(call.id)"><Square :size="12" />{{ t(canceling.includes(call.id) ? 'stopping' : 'stop') }}</button>
          <p v-if="call.error" class="error-text mt-3">{{ call.error.message }}</p>
          <JsonBlock v-if="call.result" :value="call.result.output" />
          <InteractionCard v-for="item in Object.values(runtime.interactions).filter(item => item.scope?.operation?.invocationId === call.id)" :key="item.id" :interaction="item" />
          <details v-for="state in Object.values(runtime.interactionStates).filter(item => item.scope?.operation?.invocationId === call.id)" :key="state.id" class="host-state"><summary>{{ state.description || state.name }}</summary><JsonBlock :value="state.values" /></details>
        </article>
      </section>
    </div>
  </div>
</template>

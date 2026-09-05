<script setup lang="ts">
import { onBeforeUnmount, reactive, ref, useId } from 'vue'
import { MessageCircleQuestion, LoaderCircle, ShieldCheck } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Interaction } from '../protocol'
import { useRuntime } from '../stores/runtime'
import { APIError, errorMessage } from '../api'
import { interactionValues } from '../forms'
import FieldControl from './FieldControl.vue'
import MarkdownContent from './MarkdownContent.vue'
const props = defineProps<{ interaction: Interaction }>()
const runtime = useRuntime()
const { t } = useI18n()
// The same request can appear inline and in the global drawer simultaneously.
const formId = useId()
const values = reactive<Record<string, unknown>>({})
const busy = ref(false)
const error = ref('')
const settled = ref(false)
async function submit() {
  error.value = ''
  let result: Record<string, unknown>
  try { result = interactionValues(props.interaction.fields, values) }
  catch (cause) { error.value = t(errorMessage(cause)); return }
  busy.value = true
  try { await runtime.respond(props.interaction.id, result); settled.value = true }
  catch (cause) { error.value = cause instanceof APIError && cause.status === 409 ? t('settled') : errorMessage(cause) }
  finally { busy.value = false }
}
onBeforeUnmount(() => { for (const key of Object.keys(values)) delete values[key] })
</script>
<template>
  <form :id="'request-' + interaction.id + '-' + formId" class="interaction-card" @submit.prevent="submit">
    <div class="flex items-center gap-2 text-sm font-semibold accent">
      <ShieldCheck v-if="interaction.level === 'warning'" :size="17" /><MessageCircleQuestion v-else :size="17" />
      <span>{{ t(settled ? 'settled' : 'waiting') }}</span>
    </div>
    <MarkdownContent v-if="interaction.description" :text="interaction.description" />
    <FieldControl v-for="field in interaction.fields" :id="formId + '-' + field.name" :key="field.name" v-model="values[field.name]" :field="field" :disabled="busy || settled" />
    <p v-if="error" role="alert" class="error-text">{{ error }}</p>
    <button class="btn primary" type="submit" :disabled="busy || settled || runtime.connection !== 'online'"><LoaderCircle v-if="busy" class="spin" :size="15" />{{ t('submit') }}</button>
  </form>
</template>

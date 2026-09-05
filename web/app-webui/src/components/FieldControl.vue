<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { InteractionField } from '../protocol'
const props = defineProps<{ field: InteractionField; modelValue?: unknown; id: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: unknown] }>()
const { t } = useI18n()
function toggle(value: string, checked: boolean) {
  const selected = Array.isArray(props.modelValue) ? props.modelValue as string[] : []
  emit('update:modelValue', checked ? [...selected, value] : selected.filter(item => item !== value))
}
</script>
<template>
  <div class="form-field">
    <label :id="id + '-label'" :for="id" class="field-label">{{ field.label || field.name }}<span v-if="field.required" aria-hidden="true" class="accent ml-1">*</span></label>
    <p v-if="field.description" :id="id + '-description'" class="muted text-xs">{{ field.description }}</p>
    <p v-if="field.sensitive && field.hasDefault" class="muted text-xs">{{ t('defaultPresent') }}</p>
    <p v-else-if="field.hasDefault" class="muted text-xs">{{ t('defaultValue', { value: JSON.stringify(field.default) }) }}</p>
    <div v-if="field.kind === 'choice'" :id="id" role="radiogroup" :aria-labelledby="id + '-label'" class="choice-list">
      <label v-for="option in field.options" :key="option.value" class="choice" :class="{ selected: modelValue === option.value }">
        <input type="radio" :name="id" :value="option.value" :checked="modelValue === option.value" :disabled="disabled" @change="emit('update:modelValue', option.value)" />
        <span><span class="font-medium">{{ option.label || option.value }}</span><small v-if="option.description">{{ option.description }}</small></span>
      </label>
    </div>
    <div v-else-if="field.kind === 'multichoice'" :id="id" role="group" :aria-labelledby="id + '-label'" class="choice-list">
      <label v-for="option in field.options" :key="option.value" class="choice">
        <input type="checkbox" :checked="Array.isArray(modelValue) && modelValue.includes(option.value)" :disabled="disabled" @change="toggle(option.value, ($event.target as HTMLInputElement).checked)" />
        <span><span class="font-medium">{{ option.label || option.value }}</span><small v-if="option.description">{{ option.description }}</small></span>
      </label>
    </div>
    <select v-else-if="field.kind === 'boolean'" :id="id" class="field" :value="modelValue === undefined ? '' : String(modelValue)" :disabled="disabled" @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value === '' ? undefined : ($event.target as HTMLSelectElement).value === 'true')">
      <option value="">{{ t('choose') }}</option><option value="true">{{ t('yes') }}</option><option value="false">{{ t('no') }}</option>
    </select>
    <template v-else>
      <div v-if="field.kind === 'string' && field.options?.length" class="suggestions">
        <button v-for="option in field.options" :key="option.value" type="button" class="btn small" :disabled="disabled" :title="option.description" @click="emit('update:modelValue', option.value)">{{ option.label || option.value }}</button>
      </div>
      <input v-if="field.sensitive || field.kind === 'number' || field.kind === 'integer'" :id="id" class="field" :type="field.sensitive ? 'password' : 'text'" :inputmode="field.kind === 'integer' ? 'numeric' : field.kind === 'number' ? 'decimal' : 'text'" autocomplete="off" :value="modelValue ?? ''" :disabled="disabled" @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)" />
      <textarea v-else :id="id" class="field" rows="2" :value="String(modelValue ?? '')" :disabled="disabled" :placeholder="field.options?.length ? t('freeText') : undefined" @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)" />
    </template>
    <button v-if="field.hasDefault && modelValue !== undefined" type="button" class="text-button" :disabled="disabled" @click="emit('update:modelValue', undefined)">{{ t('useDefault') }}</button>
  </div>
</template>

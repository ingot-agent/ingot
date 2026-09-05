<script setup lang="ts">
import { useI18n } from 'vue-i18n'
defineProps<{ status: string }>()
const { t } = useI18n()
</script>
<template><span class="status-badge" :class="'status-' + status"><i />{{ t('status.' + status) }}</span></template>

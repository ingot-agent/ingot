<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import MarkdownIt from 'markdown-it'
import hljs from 'highlight.js/lib/common'
import { errorMessage } from '../api'
const props = defineProps<{ text: string }>()
const { t } = useI18n()
const copyError = ref('')
const markdown = new MarkdownIt({
  html: false, linkify: true, breaks: true,
  highlight(source, language) {
    if (language && hljs.getLanguage(language)) {
      return hljs.highlight(source, { language, ignoreIllegals: true }).value
    }
    return ''
  },
})
const originalFence = markdown.renderer.rules.fence!
markdown.renderer.rules.fence = (tokens, index, options, env, self) => {
  const label = markdown.utils.escapeHtml(t('copy'))
  const language = markdown.utils.escapeHtml(tokens[index].info.trim().split(/\s+/)[0])
  return '<div class="code-block"><div class="code-toolbar"><span>' + language +
    '</span><button type="button" class="text-button" data-copy-code>' + label + '</button></div>' +
    originalFence(tokens, index, options, env, self) + '</div>'
}
const originalLink = markdown.renderer.rules.link_open
markdown.renderer.rules.link_open = (tokens, index, options, env, self) => {
  tokens[index].attrSet('target', '_blank')
  tokens[index].attrSet('rel', 'noopener noreferrer')
  return originalLink ? originalLink(tokens, index, options, env, self) : self.renderToken(tokens, index, options)
}
// Remote images are links until explicitly opened, avoiding background fetches.
markdown.renderer.rules.image = (tokens, index) => {
  const token = tokens[index]
  const src = token.attrGet('src') || ''
  const text = markdown.utils.escapeHtml(token.content || src)
  return /^https?:\/\//i.test(src)
    ? '<a target="_blank" rel="noopener noreferrer" href="' + markdown.utils.escapeHtml(src) + '">' + text + '</a>'
    : text
}
const html = computed(() => markdown.render(props.text))
async function copyCode(event: MouseEvent) {
  const button = event.target instanceof Element ? event.target.closest<HTMLButtonElement>('button[data-copy-code]') : null
  if (!button || !(event.currentTarget as HTMLElement).contains(button)) return
  const source = button.parentElement?.nextElementSibling?.textContent
  if (source === undefined || source === null) return
  copyError.value = ''
  try {
    await navigator.clipboard.writeText(source)
    button.textContent = t('copied')
  } catch (error) { copyError.value = errorMessage(error) }
}
</script>
<!-- markdown-it disables HTML and validates link protocols before rendering. -->
<template>
  <!-- eslint-disable-next-line vue/no-v-html -->
  <div class="markdown" @click="copyCode" v-html="html" />
  <p v-if="copyError" class="error-text" role="alert">{{ copyError }}</p>
</template>

import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import MarkdownContent from './MarkdownContent.vue'
import { i18n } from '../i18n'

let wrapper: VueWrapper
afterEach(() => { wrapper?.unmount(); vi.unstubAllGlobals() })
describe('untrusted Markdown', () => {
  it('disables raw HTML, unsafe links, and background remote image requests', () => {
    wrapper = mount(MarkdownContent, { props: { text: '<img src=x onerror=alert(1)>\n\n[bad](javascript:alert(1))\n\n![remote](https://example.com/pixel.png)' }, global: { plugins: [i18n] } })
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.find('a[href^="javascript:"]').exists()).toBe(false)
    expect(wrapper.get('a').attributes('href')).toBe('https://example.com/pixel.png')
    expect(wrapper.get('a').attributes('rel')).toBe('noopener noreferrer')
  })
  it('copies original code, without syntax highlighting markup', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    wrapper = mount(MarkdownContent, { props: { text: '```js\nconst value = "<safe>"\n```' }, global: { plugins: [i18n] } })
    await wrapper.get('[data-copy-code]').trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith('const value = "<safe>"\n')
    expect(wrapper.find('safe').exists()).toBe(false)
  })
})

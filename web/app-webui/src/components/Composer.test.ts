import { afterEach, describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import Composer from './Composer.vue'
import { useRuntime } from '../stores/runtime'
import { i18n } from '../i18n'

let wrapper: VueWrapper
function composer() {
  const pinia = createPinia()
  useRuntime(pinia).connection = 'online'
  wrapper = mount(Composer, { props: { sessionKey: 'new', running: [] }, global: { plugins: [pinia, i18n] } })
  return wrapper
}
afterEach(() => wrapper?.unmount())

describe('composer drafts', () => {
  it('does not send Enter used to confirm IME composition or add a newline', async () => {
    const view = composer()
    const input = view.get('textarea')
    await input.setValue('你好')
    await input.trigger('keydown', { key: 'Enter', isComposing: true })
    await input.trigger('keydown', { key: 'Enter', keyCode: 229 })
    await input.trigger('keydown', { key: 'Enter', shiftKey: true })
    expect(view.emitted('send')).toBeUndefined()
    await input.trigger('keydown', { key: 'Enter' })
    expect(view.emitted('send')?.[0][0]).toBe('你好')
  })

  it('retains a draft after lazy session creation until sending is accepted', async () => {
    const view = composer()
    await view.get('textarea').setValue('keep this draft')
    await view.get('form').trigger('submit')
    await view.setProps({ sending: true, sessionKey: 'created-session' })
    await view.setProps({ sending: false })
    expect(view.get('textarea').element.value).toBe('keep this draft')
    const accepted = view.emitted('send')![0][2] as () => void
    accepted()
    await view.vm.$nextTick()
    expect(view.get('textarea').element.value).toBe('')
  })

  it('does not clear a different session draft when an earlier send completes', async () => {
    const view = composer()
    await view.setProps({ sessionKey: 'first' })
    await view.get('textarea').setValue('first draft')
    await view.get('form').trigger('submit')
    await view.setProps({ sessionKey: 'second' })
    await view.get('textarea').setValue('second draft')
    const accepted = view.emitted('send')![0][2] as () => void
    accepted()
    await view.vm.$nextTick()
    expect(view.get('textarea').element.value).toBe('second draft')
    await view.setProps({ sessionKey: 'first' })
    expect(view.get('textarea').element.value).toBe('')
  })
})

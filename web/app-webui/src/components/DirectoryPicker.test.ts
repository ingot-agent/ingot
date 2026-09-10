import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import DirectoryPicker from './DirectoryPicker.vue'
import { i18n } from '../i18n'

let wrapper: VueWrapper
afterEach(() => { wrapper?.unmount(); vi.unstubAllGlobals() })

describe('DirectoryPicker', () => {
  it('can switch from one Windows drive root to another', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        path: 'C:\\', roots: [{ name: 'D:\\', path: 'D:\\' }], directories: [],
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        path: 'D:\\', roots: [{ name: 'C:\\', path: 'C:\\' }], directories: [],
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetch)
    wrapper = mount(DirectoryPicker, {
      props: { open: false, initialPath: 'C:\\' },
      global: {
        plugins: [i18n],
        stubs: { Overlay: { template: '<div><slot/><slot name="footer"/></div>' } },
      },
    })

    await wrapper.setProps({ open: true })
    await flushPromises()
    const drive = wrapper.findAll('button').find(button => button.text() === 'D:\\')
    expect(drive).toBeDefined()
    await drive!.trigger('click')
    await flushPromises()

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/workspace/browse?path=C%3A%5C', { cache: 'no-store' })
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/workspace/browse?path=D%3A%5C', { cache: 'no-store' })
    expect(wrapper.text()).toContain('D:\\')
  })
})

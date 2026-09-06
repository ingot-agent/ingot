import { describe, expect, it } from 'vitest'
import { copyableText } from './copy'
import type { Message } from './protocol'

describe('copyableText', () => {
  it('joins text parts', () => {
    const message: Message = { role: 'assistant', content: [{ kind: 'text', text: 'alpha' }, { kind: 'text', text: 'beta' }] }
    expect(copyableText(message)).toBe('alpha\nbeta')
  })
  it('returns empty for a tool-only assistant message', () => {
    const message: Message = { role: 'assistant', content: [], toolCalls: [{ id: 'c', name: 'shell_exec', arguments: {} }] }
    expect(copyableText(message)).toBe('')
  })
  it('ignores empty and non-text parts', () => {
    const message: Message = { role: 'assistant', content: [{ kind: 'text', text: '' }, { kind: 'file', text: undefined }] }
    expect(copyableText(message)).toBe('')
  })
})

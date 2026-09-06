import { describe, expect, it } from 'vitest'
import { copyableText, turnCopyTexts } from './copy'
import type { Message, ToolCall } from './protocol'

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

describe('turnCopyTexts', () => {
  const assistant = (text: string, toolCalls: ToolCall[] = []): Message => ({ role: 'assistant', content: text ? [{ kind: 'text', text }] : [], toolCalls })
  const tool = (id: string): Message => ({ role: 'tool', toolCallId: id, content: [{ kind: 'text', text: id }] })
  const user = (text: string): Message => ({ role: 'user', content: [{ kind: 'text', text }] })

  it('groups consecutive assistant text into a single turn anchored at the last text message', () => {
    const messages: Message[] = [
      user('Q1'),
      assistant('Checking...', [{ id: 'c1', name: 'shell_exec', arguments: {} }]),
      tool('c1'),
      assistant('The answer is here.'),
    ]
    expect(turnCopyTexts(messages)).toEqual(new Map([[3, 'Checking...\nThe answer is here.']]))
  })

  it('anchors the copy button on the final text-bearing assistant message, not a tool-only one', () => {
    const messages: Message[] = [
      user('Q1'),
      assistant('Commentary before tool', [{ id: 'c1', name: 'shell_exec', arguments: {} }]),
      tool('c1'),
      assistant('', [{ id: 'c2', name: 'shell_exec', arguments: {} }]),
      tool('c2'),
      assistant('Final summary.'),
    ]
    expect(turnCopyTexts(messages)).toEqual(new Map([[5, 'Commentary before tool\nFinal summary.']]))
  })

  it('separates turns at user messages', () => {
    const messages: Message[] = [
      user('Q1'),
      assistant('Answer 1.'),
      user('Q2'),
      assistant('Answer 2.'),
    ]
    expect(turnCopyTexts(messages)).toEqual(new Map([[1, 'Answer 1.'], [3, 'Answer 2.']]))
  })

  it('skips a turn with no assistant text', () => {
    const messages: Message[] = [
      user('Q1'),
      assistant('', [{ id: 'c1', name: 'shell_exec', arguments: {} }]),
      tool('c1'),
    ]
    expect(turnCopyTexts(messages)).toEqual(new Map())
  })
})

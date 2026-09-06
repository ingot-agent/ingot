import { describe, expect, it } from 'vitest'
import { titleForFirstMessage } from './title'

describe('titleForFirstMessage', () => {
  it('returns a trimmed single-line title for a normal message', () => {
    expect(titleForFirstMessage('  hello world  ')).toBe('hello world')
  })
  it('collapses line breaks and repeated whitespace', () => {
    expect(titleForFirstMessage('line one\nline two\t\ttabbed')).toBe('line one line two tabbed')
  })
  it('truncates long input and appends an ellipsis', () => {
    const input = 'a'.repeat(120)
    expect(titleForFirstMessage(input)).toBe('a'.repeat(60) + '…')
  })
  it('trims trailing whitespace before truncating', () => {
    const input = 'a'.repeat(55) + ' '.repeat(20)
    expect(titleForFirstMessage(input)).toBe('a'.repeat(55))
  })
  it('returns empty for whitespace-only input', () => {
    expect(titleForFirstMessage('   \n\t ')).toBe('')
  })
  it('returns empty for empty input', () => {
    expect(titleForFirstMessage('')).toBe('')
  })
})

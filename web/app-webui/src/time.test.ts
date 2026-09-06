import { describe, expect, it } from 'vitest'
import { workspaceBasename } from './time'

describe('workspaceBasename', () => {
  it('returns the trailing folder name for a unix path', () => {
    expect(workspaceBasename('/home/me/projects/ingot')).toBe('ingot')
  })
  it('returns the trailing folder name for a windows path', () => {
    expect(workspaceBasename('C:\\Users\\me\\deepseek-harness')).toBe('deepseek-harness')
  })
  it('ignores trailing separators', () => {
    expect(workspaceBasename('/repo/project/')).toBe('project')
  })
  it('returns empty when no path is present', () => {
    expect(workspaceBasename('')).toBe('')
  })
})

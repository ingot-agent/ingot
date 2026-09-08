import { describe, expect, it } from 'vitest'
import { shouldShowHistoryMessage, shouldShowTurnByline } from './conversationDisplay'
import type { Interaction, LiveTurn, Message } from '../protocol'

const toolMessage: Message = { role: 'assistant', content: [], toolCalls: [{ id: 'call', name: 'inspect', arguments: {} }] }
const answerWithTool: Message = { role: 'assistant', content: [{ kind: 'text', text: 'I will inspect this.' }], toolCalls: toolMessage.toolCalls }
const interaction: Interaction = { id: 'question', name: 'ask', fields: [] }
const toolOnlyTurn: LiveTurn = { id: 'turn', sessionId: 'session', revision: 0, output: '', reasoning: '', status: 'running', blocks: [{ id: 'tool', kind: 'tool', call: toolMessage.toolCalls![0], status: 'running' }] }

describe('conversation display projection', () => {
  it('hides only tool-only history messages when tool calls are hidden', () => {
    expect(shouldShowHistoryMessage(toolMessage, false)).toBe(false)
    expect(shouldShowHistoryMessage(toolMessage, true)).toBe(true)
    expect(shouldShowHistoryMessage(answerWithTool, false)).toBe(true)
  })

  it('keeps a live turn byline for visible output and interactions', () => {
    expect(shouldShowTurnByline(toolOnlyTurn, false, {})).toBe(false)
    expect(shouldShowTurnByline(toolOnlyTurn, true, {})).toBe(true)

    const turnWithInteraction: LiveTurn = { ...toolOnlyTurn, blocks: [...toolOnlyTurn.blocks!, { id: 'interaction', kind: 'interaction', interactionId: 'question' }] }
    expect(shouldShowTurnByline(turnWithInteraction, false, { question: interaction })).toBe(true)
  })
})

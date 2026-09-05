import { describe, expect, it } from 'vitest'
import { bootstrapTurns, reduceOperation, reduceTurn } from './state'
import type { LiveTurn, OperationInvocation, Snapshot, WebEvent } from './protocol'

const event = (type: string, data: WebEvent['data']): WebEvent => ({ type, data, scope: { agent: { sessionId: 's' } } })
describe('authoritative turn projections', () => {
  it('ignores replay overlapping a bootstrap and shares revisions across output and reasoning', () => {
    const turns = bootstrapTurns({ turns: [{ id: 'web', sessionId: 's', revision: 4, reasoning: 'thinking', output: 'hello' }] } as Snapshot)
    reduceTurn(turns, event('agent.invocation.started', { id: 'web', sessionId: 's', revision: 0, output: '', reasoning: '' }))
    reduceTurn(turns, event('agent.output.delta', { invocationId: 'web', revision: 4, text: 'hello' }))
    reduceTurn(turns, event('agent.reasoning.delta', { invocationId: 'web', revision: 5, text: '!' }))
    reduceTurn(turns, event('agent.output.delta', { invocationId: 'web', revision: 6, text: ' world' }))
    expect(turns.web.output).toBe('hello world')
    expect(turns.web.reasoning).toBe('thinking!')
    expect(turns.web.revision).toBe(6)
  })
  it('handles failure before SDK execution and cannot be revived by replay or late observations', () => {
    const turns: Record<string, LiveTurn> = {}
    reduceTurn(turns, event('agent.invocation.finished', { invocationId: 'web', status: 'failed', error: { code: 'invalid_turn', message: 'Invalid' } }))
    reduceTurn(turns, event('agent.invocation.started', { id: 'web' }))
    reduceTurn(turns, event('agent.output.delta', { invocationId: 'web', revision: 1, text: 'late' }))
    reduceTurn(turns, event('agent.turn.finished', { status: 'succeeded' }))
    expect(turns.web.status).toBe('failed')
    expect(turns.web.output).toBe('')
    expect(turns.web.error?.message).toBe('Invalid')
  })
  it('preserves partial output and canonical multimodal output independently', () => {
    const turns: Record<string, LiveTurn> = { web: { id: 'web', sessionId: 's', revision: 2, output: 'partial', reasoning: '', status: 'running' } }
    reduceTurn(turns, event('agent.invocation.finished', { invocationId: 'web', status: 'succeeded', result: { output: [{ kind: 'image', source: { kind: 'asset', assetId: 'a' } }] } }))
    expect(turns.web.output).toBe('partial')
    expect(turns.web.result?.output[0].kind).toBe('image')
  })
  it('does not regress terminal operations during overlapping replay', () => {
    const operations: Record<string, OperationInvocation> = { op: { id: 'op', name: 'test', status: 'succeeded', result: { output: {} } } }
    reduceOperation(operations, event('operation.started', { id: 'op', name: 'test', status: 'running' }))
    expect(operations.op.status).toBe('succeeded')
  })

  it('appends interleaved reasoning, text, tools and requests while updating tools in place', () => {
    const turns = bootstrapTurns({ turns: [{ id: 'web', sessionId: 's', revision: 0, reasoning: '', output: '' }] } as Snapshot)
    const scope = { agent: { sessionId: 's', turnId: 'sdk', toolCallId: 'call' } }
    reduceTurn(turns, event('agent.reasoning.delta', { invocationId: 'web', revision: 1, text: 'First thought' }))
    reduceTurn(turns, event('agent.reasoning.delta', { invocationId: 'web', revision: 2, text: '.' }))
    reduceTurn(turns, event('agent.output.delta', { invocationId: 'web', revision: 3, text: 'Checking.' }))
    reduceTurn(turns, { type: 'agent.tool.started', scope, data: { call: { id: 'call', name: 'inspect', arguments: {} } } })
    reduceTurn(turns, event('agent.reasoning.delta', { invocationId: 'web', revision: 4, text: 'Second thought.' }))
    reduceTurn(turns, { type: 'interaction.requested', scope, data: { id: 'question' } })
    reduceTurn(turns, { type: 'agent.tool.progress', scope, data: { progress: { content: [{ kind: 'text', text: 'Progress' }] } } })
    reduceTurn(turns, { type: 'agent.tool.finished', scope, data: { status: 'succeeded', result: { content: [{ kind: 'text', text: 'Done' }] } } })
    reduceTurn(turns, event('agent.reasoning.delta', { invocationId: 'web', revision: 5, text: 'Third thought.' }))
    reduceTurn(turns, event('agent.output.delta', { invocationId: 'web', revision: 6, text: 'Answer.' }))
    expect(turns.web.blocks?.map(block => block.kind)).toEqual(['reasoning', 'output', 'tool', 'reasoning', 'interaction', 'reasoning', 'output'])
    expect(turns.web.blocks?.[0]).toMatchObject({ text: 'First thought.' })
    expect(turns.web.blocks?.[2]).toMatchObject({ status: 'succeeded', content: [{ kind: 'text', text: 'Done' }] })
    expect(turns.web.output).toBe('Checking.Answer.')
    expect(turns.web.sdkTurnId).toBe('sdk')
  })

  it('starts a new reasoning block for a new model invocation without a visible separator', () => {
    const turns = bootstrapTurns({ turns: [{ id: 'web', sessionId: 's', revision: 0, reasoning: '', output: '' }] } as Snapshot)
    reduceTurn(turns, event('agent.reasoning.delta', { invocationId: 'web', revision: 1, text: 'First attempt' }))
    reduceTurn(turns, event('agent.model.started', {}))
    reduceTurn(turns, event('agent.reasoning.delta', { invocationId: 'web', revision: 2, text: 'Second attempt' }))
    expect(turns.web.blocks).toHaveLength(2)
    expect(turns.web.blocks?.[1]).toMatchObject({ kind: 'reasoning', text: 'Second attempt' })
  })

  it('restores aggregate snapshot text and pending requests without merging new reasoning into the snapshot', () => {
    const turns = bootstrapTurns({
      turns: [{ id: 'web', sessionId: 's', revision: 4, reasoning: 'Earlier thoughts', output: 'Earlier output' }],
      interactions: [{ id: 'pending', scope: { agent: { sessionId: 's', turnId: 'sdk' } } }],
    } as Snapshot)
    reduceTurn(turns, event('agent.output.delta', { invocationId: 'web', revision: 4, text: 'Duplicate' }))
    reduceTurn(turns, event('agent.reasoning.delta', { invocationId: 'web', revision: 5, text: 'New thought' }))
    expect(turns.web.blocks?.map(block => block.kind)).toEqual(['reasoning', 'output', 'interaction', 'reasoning'])
    expect(turns.web.blocks?.[3]).toMatchObject({ text: 'New thought' })
  })

  it('does not attach late SDK events to the next invocation or mix sessions and operations', () => {
    const turns = bootstrapTurns({ turns: [{ id: 'web', sessionId: 's', revision: 0, reasoning: '', output: '' }] } as Snapshot)
    const scope = { agent: { sessionId: 's', turnId: 'sdk', toolCallId: 'call' } }
    reduceTurn(turns, { type: 'agent.turn.started', scope, data: {} })
    reduceTurn(turns, event('agent.invocation.finished', { invocationId: 'web', status: 'canceled' }))
    reduceTurn(turns, event('agent.invocation.started', { id: 'next', sessionId: 's', revision: 0, reasoning: '', output: '' }))
    const data = { call: { id: 'call', name: 'inspect', arguments: {} } }
    reduceTurn(turns, { type: 'agent.tool.started', scope, data })
    reduceTurn(turns, { type: 'agent.tool.started', scope: { agent: { sessionId: 'another' } }, data })
    reduceTurn(turns, { type: 'interaction.requested', scope: { agent: { sessionId: 's' }, operation: { invocationId: 'op' } }, data: { id: 'op-question' } })
    expect(turns.web.blocks).toEqual([])
    expect(turns.next.blocks).toEqual([])
  })
})

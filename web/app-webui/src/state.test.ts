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
})

import type { LiveTurn, OperationInvocation, Snapshot, WebEvent } from './protocol'

export const indexById = <T extends { id: string }>(items: T[] | null | undefined): Record<string, T> =>
  Object.fromEntries((items || []).map(item => [item.id, item]))

export function bootstrapTurns(snapshot: Snapshot): Record<string, LiveTurn> {
  return indexById((snapshot.turns || []).map(turn => ({ ...turn, status: 'running' as const })))
}

export function reduceTurn(turns: Record<string, LiveTurn>, event: WebEvent) {
  const data = event.data
  if (event.type === 'agent.invocation.started') {
    const previous = turns[data.id]
    // Replay can overlap bootstrap, and observation can arrive after completion.
    if (!previous) turns[data.id] = { ...(data as LiveTurn), status: 'running' }
  } else if (event.type === 'agent.output.delta' || event.type === 'agent.reasoning.delta') {
    const turn = turns[data.invocationId]
    if (!turn || turn.status !== 'running' || data.revision <= turn.revision) return
    turn.revision = data.revision
    const key = event.type === 'agent.output.delta' ? 'output' : 'reasoning'
    turn[key] += data.text
  } else if (event.type === 'agent.invocation.finished') {
    const id = data.invocationId
    const previous = turns[id]
    turns[id] = {
      ...previous,
      id, sessionId: event.scope?.agent?.sessionId || previous?.sessionId || '',
      revision: previous?.revision || 0, output: previous?.output || '', reasoning: previous?.reasoning || '',
      status: data.status, stopping: false, outcome: data.outcome,
      result: data.result, error: data.error,
    }
  }
}

export function reduceOperation(items: Record<string, OperationInvocation>, event: WebEvent) {
  const next = event.data as OperationInvocation
  if (event.type === 'operation.started' && items[next.id]?.status !== undefined && items[next.id].status !== 'running') return
  items[next.id] = next
}

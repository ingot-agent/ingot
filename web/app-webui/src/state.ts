import type { LiveTurn, OperationInvocation, Snapshot, Turn, TurnBlock, WebEvent } from './protocol'

export const indexById = <T extends { id: string }>(items: T[] | null | undefined): Record<string, T> =>
  Object.fromEntries((items || []).map(item => [item.id, item]))

export function bootstrapTurns(snapshot: Snapshot): Record<string, LiveTurn> {
  const turns = indexById((snapshot.turns || []).map(turn => ({ ...turn, blocks: snapshotBlocks(turn), status: 'running' as const })))
  for (const interaction of snapshot.interactions || []) {
    reduceTurn(turns, { type: 'interaction.requested', scope: interaction.scope, data: interaction })
  }
  return turns
}

function snapshotBlocks(turn: Turn): TurnBlock[] {
  // Snapshots only have aggregate text; chronology starts with this connection.
  const blocks: TurnBlock[] = []
  if (turn.reasoning) blocks.push({ id: 'snapshot-reasoning', kind: 'reasoning', text: turn.reasoning, boundary: -1 })
  if (turn.output) blocks.push({ id: 'snapshot-output', kind: 'output', text: turn.output, boundary: -1 })
  return blocks
}

function activityTurn(turns: Record<string, LiveTurn>, event: WebEvent): LiveTurn | undefined {
  const scope = event.scope?.agent || event.data.scope?.agent
  if (!scope?.sessionId || event.scope?.operation || event.data.scope?.operation) return
  const candidates = Object.values(turns).filter(turn => turn.sessionId === scope.sessionId)
  const matched = scope.turnId && candidates.find(turn => turn.sdkTurnId === scope.turnId)
  if (matched) return matched.status === 'running' ? matched : undefined
  const running = candidates.filter(turn => turn.status === 'running' && (!scope.turnId || !turn.sdkTurnId))
  // Web invocation IDs and SDK turn IDs are different. Only associate an
  // observation when the session has an unambiguous active invocation.
  if (running.length !== 1) return
  const turn = running[0]
  if (scope.turnId) turn.sdkTurnId = scope.turnId
  return turn
}

export function reduceTurn(turns: Record<string, LiveTurn>, event: WebEvent) {
  const data = event.data
  if (event.type === 'agent.invocation.started') {
    const previous = turns[data.id]
    // Replay can overlap bootstrap, and observation can arrive after completion.
    if (!previous) turns[data.id] = { ...(data as LiveTurn), blocks: snapshotBlocks(data as Turn), status: 'running' }
  } else if (event.type === 'agent.output.delta' || event.type === 'agent.reasoning.delta') {
    const turn = turns[data.invocationId]
    if (!turn || turn.status !== 'running' || data.revision <= turn.revision) return
    turn.revision = data.revision
    const key = event.type === 'agent.output.delta' ? 'output' : 'reasoning'
    const blocks = turn.blocks ||= snapshotBlocks(turn)
    turn[key] += data.text
    if (!data.text) return
    const boundary = turn.streamBoundary || 0
    const last = blocks.at(-1)
    if (last?.kind === key && last.boundary === boundary) last.text += data.text
    else blocks.push({ id: 'delta-' + data.revision, kind: key, text: data.text, boundary })
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
  } else if (/^agent\.(turn|round|model|tool)\./.test(event.type) || event.type === 'interaction.requested') {
    const turn = activityTurn(turns, event)
    if (!turn) return
    const blocks = turn.blocks ||= snapshotBlocks(turn)
    if (event.type === 'agent.round.started' || event.type === 'agent.model.started') {
      turn.streamBoundary = (turn.streamBoundary || 0) + 1
    } else if (event.type === 'interaction.requested') {
      if (!blocks.some(block => block.kind === 'interaction' && block.interactionId === data.id)) {
        blocks.push({ id: 'interaction-' + data.id, kind: 'interaction', interactionId: data.id })
      }
    } else if (event.type.startsWith('agent.tool.')) {
      const id = event.scope?.agent?.toolCallId || data.call?.id
      if (!id) return
      const block = blocks.find(block => block.kind === 'tool' && block.call.id === id)
      if (event.type === 'agent.tool.started' && !block && data.call) {
        blocks.push({ id: 'tool-' + id, kind: 'tool', call: data.call, status: 'running' })
      } else if (block?.kind === 'tool') {
        if (event.type === 'agent.tool.progress') block.content = [...(block.content || []), ...(data.progress?.content || [])]
        else if (event.type === 'agent.tool.finished') {
          block.status = data.status
          block.content = data.result?.content ?? block.content
          block.error = data.error
        }
      }
    }
  }
}

export function reduceOperation(items: Record<string, OperationInvocation>, event: WebEvent) {
  const next = event.data as OperationInvocation
  if (event.type === 'operation.started' && items[next.id]?.status !== undefined && items[next.id].status !== 'running') return
  items[next.id] = next
}

import type { Interaction, LiveTurn, Message } from '../protocol'

export function hasVisibleMessageContent(message: Message) {
  return message.content.some(part => part.kind !== 'text' || Boolean(part.text?.trim()))
}

export function shouldShowHistoryMessage(message: Message, showToolCalls: boolean) {
  const isToolOnlyAssistant = message.role === 'assistant' && Boolean(message.toolCalls?.length) && !hasVisibleMessageContent(message)
  return showToolCalls || !isToolOnlyAssistant
}

export function shouldShowTurnByline(turn: LiveTurn, showToolCalls: boolean, interactions: Record<string, Interaction>) {
  if (showToolCalls) return true
  const blocks = turn.blocks || []
  if (!blocks.some(block => block.kind === 'tool')) return true
  if (turn.output || turn.reasoning || turn.result?.output?.length || turn.error) return true
  return blocks.some(block => block.kind !== 'tool' && (block.kind !== 'interaction' || Boolean(interactions[block.interactionId])))
}

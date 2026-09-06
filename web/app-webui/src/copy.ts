import type { Message } from './protocol'

// copyableText returns the concatenated text parts of a message. Messages
// whose content is only tool calls, files, or other non-text parts produce an
// empty string and should not render a copy button.
export function copyableText(message: Message): string {
  return message.content.filter(part => part.kind === 'text' && part.text).map(part => part.text).join('\n')
}

// turnCopyTexts groups the assistant text of each conversation turn and returns
// a map keyed by the index of the last text-bearing assistant message in that
// turn. A turn is delimited by user messages, so consecutive assistant messages
// (with the tool messages between them) belong to the same turn. This lets the
// copy button placed on the final assistant message of a turn copy the whole
// turn's assistant text instead of a single fragment (e.g. commentary that
// precedes a tool call).
export function turnCopyTexts(messages: Message[]): Map<number, string> {
  const result = new Map<number, string>()
  const texts: string[] = []
  let lastTextAssistant = -1
  const flush = () => {
    if (lastTextAssistant >= 0 && texts.length) result.set(lastTextAssistant, texts.join('\n'))
    texts.length = 0
    lastTextAssistant = -1
  }
  messages.forEach((message, index) => {
    if (message.role === 'user') flush()
    else if (message.role === 'assistant') {
      const text = copyableText(message)
      if (text) {
        lastTextAssistant = index
        texts.push(text)
      }
    }
  })
  flush()
  return result
}

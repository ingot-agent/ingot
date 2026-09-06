import type { Message } from './protocol'

// copyableText returns the concatenated text parts of a message. Messages
// whose content is only tool calls, files, or other non-text parts produce an
// empty string and should not render a copy button.
export function copyableText(message: Message): string {
  return message.content.filter(part => part.kind === 'text' && part.text).map(part => part.text).join('\n')
}

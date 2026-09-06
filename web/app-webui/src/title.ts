// titleForFirstMessage derives a conversation title from the first user
// message. The raw input may contain line breaks, leading/trailing whitespace
// and long bodies, so it is collapsed to a single line and truncated before it
// is used as a Session title. An empty result means the caller should fall back
// to the localized "new conversation" placeholder.
const FIRST_TITLE_MAX = 60

export function titleForFirstMessage(input: string): string {
  const line = input.trim().replace(/\s+/g, ' ')
  if (!line) return ''
  return line.length > FIRST_TITLE_MAX ? line.slice(0, FIRST_TITLE_MAX).trim() + '…' : line
}

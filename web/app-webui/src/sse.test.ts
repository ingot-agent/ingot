import { describe, expect, it } from 'vitest'
import { SSEDecoder } from './sse'
import type { WebEvent } from './protocol'
describe('SSE framing', () => {
  it('handles split CRLF, heartbeats, multiple records, and Unicode text', () => {
    const received: [number, WebEvent][] = []
    const decoder = new SSEDecoder((id, event) => received.push([id, event]))
    const wire = ': ping\r\n\r\nid: 2\r\ndata: {"type":"agent.output.delta",\r\ndata: "data":{"text":"你好"}}\r\n\r\nid: 3\ndata: {"type":"session.created","data":{}}\n\n'
    for (const character of wire) decoder.push(character)
    expect(received.map(item => item[0])).toEqual([2, 3])
    expect(received[0][1].data.text).toBe('你好')
  })
  it('does not dispatch an incomplete event and rejects an invalid envelope', () => {
    const received: unknown[] = []
    const decoder = new SSEDecoder((id, event) => received.push([id, event]))
    decoder.push('id: 1\ndata: {"type":"x","data":{}}\n')
    expect(received).toHaveLength(0)
    expect(() => decoder.push('\nid: NaN\ndata: {"type":"x"}\n\n')).toThrow('Invalid event')
    expect(received).toHaveLength(1)
  })
})

export type Status = 'running' | 'succeeded' | 'failed' | 'canceled'
export interface Session {
  id: string
  title: string
  createdAt: string
  updatedAt: string
  archivedAt?: string
}
export interface Part {
  kind: string
  text?: string
  mimeType?: string
  name?: string
  source?: { kind: string; data?: string; uri?: string; assetId?: string }
}
export interface ToolCall { id: string; name: string; arguments: unknown }
export interface Message {
  role: string
  content: Part[]
  name?: string
  toolCallId?: string
  toolCalls?: ToolCall[]
}
export interface Attachment { kind: string; mimeType?: string; name?: string; assetId: string }
export interface Scope {
  agent?: { sessionId?: string; turnId?: string; roundIndex?: number; toolCallId?: string }
  operation?: { invocationId: string }
}
export interface ErrorDetail { code: string; message: string }
export interface Outcome {
  status: Status
  durationNs: number
  accounting: {
    rounds: number
    modelInvocations: number
    toolCalls: number
    usage: { inputTokens: number; outputTokens: number; totalTokens: number; coverage: string }
    models?: unknown[]
  }
  failure?: { stage: string; roundIndex?: number; toolCallId?: string }
}
export interface Turn {
  id: string
  sessionId: string
  revision: number
  reasoning: string
  output: string
}
export type TurnBlock =
  | { id: string; kind: 'output' | 'reasoning'; text: string; boundary: number }
  | { id: string; kind: 'tool'; call: ToolCall; status: string; content?: Part[]; error?: string }
  | { id: string; kind: 'interaction'; interactionId: string }
export interface LiveTurn extends Turn {
  status: Status
  blocks?: TurnBlock[]
  streamBoundary?: number
  sdkTurnId?: string
  historyEnd?: number
  stopping?: boolean
  reconciled?: boolean
  outcome?: Outcome
  result?: { output: Part[] }
  error?: ErrorDetail
}
export interface InteractionField {
  name: string
  label?: string
  description?: string
  kind: 'string' | 'integer' | 'number' | 'boolean' | 'choice' | 'multichoice'
  required: boolean
  sensitive: boolean
  hasDefault: boolean
  default?: unknown
  options?: { value: string; label?: string; description?: string }[]
}
export interface Interaction {
  id: string
  name: string
  scope?: Scope
  description?: string
  level?: string
  fields: InteractionField[]
}
export interface InteractionState {
  id: string
  name: string
  scope?: Scope
  level?: string
  description?: string
  values: { name: string; label?: string; description?: string; value: unknown }[]
}
export interface Schema {
  type?: string | string[]
  properties?: Record<string, Schema>
  required?: string[]
  enum?: unknown[]
  title?: string
  description?: string
  default?: unknown
  [key: string]: unknown
}
export interface Operation { name: string; description: string; inputSchema: Schema; outputSchema: Schema }
export interface OperationInvocation {
  id: string
  name: string
  sessionId?: string
  status: Status
  result?: { output: unknown }
  error?: ErrorDetail
}
export interface Snapshot {
  cursor: number
  agent: { capabilities: { run: boolean; stream: boolean } }
  assets?: { available: boolean; maxBytes: number }
  sessions: Session[]
  turns: Turn[]
  interactions: Interaction[]
  interactionStates: InteractionState[]
  operations: Operation[]
  operationInvocations: OperationInvocation[]
}
// Observation and Operation payloads deliberately include arbitrary plugin JSON.
export interface WebEvent { type: string; scope?: Scope; data: Record<string, any> }
export interface TraceEvent extends WebEvent { cursor: number }
export interface Notice { id: number; message: string; level: string; scope?: Scope }

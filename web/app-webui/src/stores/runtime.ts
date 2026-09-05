import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { APIError, command, errorMessage, isAbort, request, segment } from '../api'
import { subscribe } from '../sse'
import { bootstrapTurns, indexById, reduceOperation, reduceTurn } from '../state'
import type { Attachment, Interaction, InteractionState, LiveTurn, Message, Notice, Operation, OperationInvocation, Session, Snapshot, TraceEvent, WebEvent } from '../protocol'

export const useRuntime = defineStore('runtime', () => {
  const sessions = ref<Session[]>([])
  const capabilities = ref({ run: false, stream: false })
  const assets = ref({ available: false, maxBytes: 0 })
  const turns = ref<Record<string, LiveTurn>>({})
  const interactions = ref<Record<string, Interaction>>({})
  const interactionStates = ref<Record<string, InteractionState>>({})
  const operations = ref<Operation[]>([])
  const operationInvocations = ref<Record<string, OperationInvocation>>({})
  const histories = ref<Record<string, Message[]>>({})
  const historyLoading = ref<Record<string, boolean>>({})
  const historyErrors = ref<Record<string, string>>({})
  const optimistic = ref<Record<string, { sessionId: string; message: Message }>>({})
  const traces = ref<Record<string, TraceEvent[]>>({})
  const notices = ref<Notice[]>([])
  const connection = ref<'connecting' | 'online' | 'reconnecting'>('connecting')
  const connectionError = ref('')
  const activeSession = ref('')
  const cursor = ref(0)
  let lifecycle: AbortController | undefined
  let epoch = 0
  let sessionRevision = 0
  let noticeId = 0
  const historyRequests = new Map<string, AbortController>()
  const orderedSessions = computed(() => [...sessions.value].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)))
  const pendingCount = computed(() => Object.keys(interactions.value).length)

  function notify(message: string, level = 'error', scope?: Notice['scope']) {
    notices.value.push({ id: ++noticeId, message, level, scope })
    notices.value = notices.value.slice(-30)
  }
  function running(sessionId: string) {
    return Object.values(turns.value).filter(turn => turn.sessionId === sessionId && turn.status === 'running')
  }
  async function loadHistory(id: string) {
    if (!id) return
    historyRequests.get(id)?.abort()
    const controller = new AbortController()
    historyRequests.set(id, controller)
    historyLoading.value[id] = true
    delete historyErrors.value[id]
    const generation = epoch
    try {
      const messages = await request<Message[]>('/sessions/' + segment(id) + '/history', { signal: controller.signal })
      if (generation !== epoch || controller.signal.aborted) return
      histories.value[id] = messages || []
      for (const turn of Object.values(turns.value)) {
        if (turn.sessionId === id && turn.status !== 'running' && !turn.reconciled) {
          turn.reconciled = true
          turn.historyEnd = messages?.length || 0
          delete optimistic.value[turn.id]
        }
      }
    } catch (error) {
      if (!isAbort(error) && generation === epoch) historyErrors.value[id] = errorMessage(error)
    } finally {
      if (historyRequests.get(id) === controller) {
        historyLoading.value[id] = false
        historyRequests.delete(id)
      }
    }
  }
  async function refreshSessions() {
    const generation = epoch
    const revision = ++sessionRevision
    try {
      const items = await request<Session[]>('/sessions')
      if (generation === epoch && revision === sessionRevision) sessions.value = items || []
    } catch (error) { if (!isAbort(error)) notify(errorMessage(error)) }
  }
  function bootstrap(snapshot: Snapshot) {
    epoch++
    sessionRevision++
    for (const controller of historyRequests.values()) controller.abort()
    historyRequests.clear()
    histories.value = {}
    historyLoading.value = {}
    historyErrors.value = {}
    cursor.value = snapshot.cursor
    sessions.value = snapshot.sessions || []
    capabilities.value = snapshot.agent.capabilities
    assets.value = snapshot.assets || { available: false, maxBytes: 0 }
    turns.value = bootstrapTurns(snapshot)
    interactions.value = indexById(snapshot.interactions)
    interactionStates.value = indexById(snapshot.interactionStates)
    operations.value = snapshot.operations || []
    operationInvocations.value = indexById(snapshot.operationInvocations)
    // Process-local identifiers may be reused after a server restart.
    traces.value = {}
    optimistic.value = {}
    if (activeSession.value) void loadHistory(activeSession.value)
  }
  function receive(id: number, event: WebEvent) {
    if (id <= cursor.value) return
    cursor.value = id
    const data = event.data || {}
    event.data = data
    reduceTurn(turns.value, event)
    if (event.type.startsWith('agent.invocation.') || event.type === 'agent.output.delta' || event.type === 'agent.reasoning.delta') {
      if (event.type === 'agent.invocation.finished') {
        const sid = event.scope?.agent?.sessionId
        if (sid && (sid === activeSession.value || histories.value[sid])) void loadHistory(sid)
        void refreshSessions()
        const settled = Object.values(turns.value).filter(turn => turn.status !== 'running')
        for (const turn of settled.slice(0, -128)) delete turns.value[turn.id]
      }
    } else if (/^agent\.(turn|round|model|tool)\./.test(event.type)) {
      const sid = event.scope?.agent?.sessionId
      if (sid) traces.value[sid] = [...(traces.value[sid] || []), { ...event, cursor: id }].slice(-500)
    } else if (event.type.startsWith('session.')) {
      sessionRevision++
      if (event.type === 'session.deleted') {
        sessions.value = sessions.value.filter(session => session.id !== data.id)
        delete histories.value[data.id]
        historyRequests.get(data.id)?.abort()
      } else {
        const item = data as Session
        sessions.value = [...sessions.value.filter(session => session.id !== item.id), item]
      }
    } else if (/^operation\.(started|completed|failed|canceled)$/.test(event.type)) {
      reduceOperation(operationInvocations.value, event)
      const settled = Object.values(operationInvocations.value).filter(item => item.status !== 'running')
      for (const item of settled.slice(0, -128)) delete operationInvocations.value[item.id]
    } else if (event.type === 'interaction.requested') {
      interactions.value[data.id] = data as Interaction
    } else if (event.type === 'interaction.resolved' || event.type === 'interaction.canceled') {
      delete interactions.value[data.id]
    } else if (event.type === 'interaction.state.set') {
      interactionStates.value[data.id] = data as InteractionState
    } else if (event.type === 'interaction.state.clear') {
      delete interactionStates.value[data.id]
    } else if (event.type === 'interaction.event') {
      notify(data.message || data.name, data.level || 'info', event.scope)
    }
  }
  async function connect() {
    if (lifecycle) return
    lifecycle = new AbortController()
    const signal = lifecycle.signal
    let attempts = 0
    while (!signal.aborted) {
      try {
        const snapshot = await request<Snapshot>('/state', { signal })
        bootstrap(snapshot)
        await subscribe(snapshot.cursor, signal, receive, () => {
          connection.value = 'online'
          connectionError.value = ''
          attempts = 0
        })
      } catch (error) {
        if (signal.aborted) break
        connectionError.value = errorMessage(error)
        if (error instanceof APIError && error.status === 409) {
          connection.value = 'reconnecting'
          continue
        }
      }
      if (signal.aborted) break
      connection.value = 'reconnecting'
      const delay = Math.min(1000 * 2 ** attempts++, 15000)
      await new Promise<void>(resolve => {
        const done = () => { clearTimeout(timer); signal.removeEventListener('abort', done); resolve() }
        const timer = setTimeout(done, delay)
        signal.addEventListener('abort', done, { once: true })
      })
    }
  }
  function disconnect() {
    lifecycle?.abort()
    lifecycle = undefined
    for (const controller of historyRequests.values()) controller.abort()
  }
  async function createSession(title: string) {
    const session = await command<Session>('/sessions', 'POST', { title })
    sessionRevision++
    sessions.value = [...sessions.value.filter(item => item.id !== session.id), session]
    return session
  }
  async function mutateSession(id: string, action: 'rename' | 'archive' | 'restore' | 'delete' | 'fork', title?: string) {
    const path = '/sessions/' + segment(id)
    const item = await command<Session | undefined>(
      path + (['archive', 'restore', 'fork'].includes(action) ? '/' + action : ''),
      action === 'rename' ? 'PATCH' : action === 'delete' ? 'DELETE' : 'POST',
      action === 'rename' || action === 'fork' ? { title: title || '' } : undefined,
    )
    sessionRevision++
    if (action === 'delete') {
      sessions.value = sessions.value.filter(session => session.id !== id)
      delete histories.value[id]
    } else if (item) {
      sessions.value = [...sessions.value.filter(session => session.id !== item.id), item]
    }
    return item
  }
  async function send(sessionId: string, input: string, attachments: Attachment[]) {
    const generation = epoch
    const result = await command<{ id: string }>('/turns', 'POST', { sessionId, input, attachments })
    if (generation !== epoch) return
    const turn = turns.value[result.id]
    if (!turn) turns.value[result.id] = { id: result.id, sessionId, revision: 0, output: '', reasoning: '', status: 'running' }
    if (!turn?.reconciled) optimistic.value[result.id] = {
      sessionId, message: { role: 'user', content: [
        ...(input ? [{ kind: 'text', text: input }] : []),
        ...attachments.map(attachment => ({ kind: attachment.kind, name: attachment.name, mimeType: attachment.mimeType, source: { kind: 'asset', assetId: attachment.assetId } })),
      ] },
    }
  }
  async function stop(turn: LiveTurn) {
    turn.stopping = true
    try { await command('/turns/' + segment(turn.id), 'DELETE') }
    catch (error) { turn.stopping = false; throw error }
  }
  async function respond(id: string, values: Record<string, unknown>) {
    try {
      await command('/interactions/' + segment(id) + '/response', 'POST', { values })
      delete interactions.value[id]
    } catch (error) {
      if (error instanceof APIError && error.status === 409) delete interactions.value[id]
      throw error
    }
  }
  async function invoke(name: string, input: string, sessionId: string) {
    // Preserve the original JSON text, including integers outside JS precision.
    return request<{ id: string }>('/operations/' + segment(name), {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: '{"sessionId":' + JSON.stringify(sessionId) + ',"input":' + input + '}',
    })
  }
  const cancelOperation = (id: string) => command('/operation-invocations/' + segment(id), 'DELETE')
  return {
    sessions, orderedSessions, capabilities, assets, turns, interactions, interactionStates,
    operations, operationInvocations, histories, historyLoading, historyErrors, optimistic,
    traces, notices, connection, connectionError, activeSession, cursor, pendingCount,
    notify, running, loadHistory, refreshSessions, bootstrap, receive, connect, disconnect,
    createSession, mutateSession, send, stop, respond, invoke, cancelOperation,
  }
})

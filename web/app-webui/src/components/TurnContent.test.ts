import { afterEach, describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { nextTick, reactive } from 'vue'
import TurnContent from './TurnContent.vue'
import { useRuntime } from '../stores/runtime'
import { i18n } from '../i18n'
import { reduceTurn } from '../state'
import type { Interaction, LiveTurn } from '../protocol'

let wrapper: VueWrapper
afterEach(() => wrapper?.unmount())

describe('chronological turn content', () => {
  it('keeps disclosure and form state as later blocks arrive, and replaces only the final answer', async () => {
    const pinia = createPinia()
    useRuntime(pinia).connection = 'online'
    const turns = reactive<Record<string, LiveTurn>>({ web: { id: 'web', sessionId: 's', revision: 0, output: '', reasoning: '', status: 'running', blocks: [] } })
    const interactions = reactive<Record<string, Interaction>>({ question: { id: 'question', name: 'ask', fields: [{ name: 'answer', label: 'Answer', kind: 'string', required: true, sensitive: false, hasDefault: false }] } })
    const scope = { agent: { sessionId: 's', turnId: 'sdk', toolCallId: 'call' } }
    const delta = (kind: 'reasoning' | 'output', text: string) => reduceTurn(turns, { type: `agent.${kind}.delta`, data: { invocationId: 'web', revision: turns.web.revision + 1, text } })
    delta('reasoning', 'First thought')
    delta('output', 'Before the tool')
    reduceTurn(turns, { type: 'agent.tool.started', scope, data: { call: { id: 'call', name: 'inspect', arguments: {} } } })
    delta('reasoning', 'Second thought')
    reduceTurn(turns, { type: 'interaction.requested', scope, data: interactions.question })
    wrapper = mount(TurnContent, { props: { turn: turns.web, interactions, historicalToolIds: new Set<string>() }, global: { plugins: [pinia, i18n] } })
    const firstReasoning = wrapper.get('.reasoning-block').element as HTMLDetailsElement
    firstReasoning.open = true
    await wrapper.get('textarea').setValue('Keep this answer')
    delta('reasoning', 'Third thought')
    delta('output', 'Partial final answer')
    await nextTick()
    expect(wrapper.findAll('.turn-content > *').map(item => item.classes()[0])).toEqual(['reasoning-block', 'markdown', 'tool-card', 'reasoning-block', 'interaction-card', 'reasoning-block', 'markdown', 'thinking-dots'])
    expect(wrapper.get('.reasoning-block').element).toBe(firstReasoning)
    expect(firstReasoning.open).toBe(true)
    expect(wrapper.get('textarea').element.value).toBe('Keep this answer')
    expect(wrapper.findAll('.reasoning-block').map(item => item.get('.markdown').text())).toEqual(['First thought', 'Second thought', 'Third thought'])
    delete interactions.question
    turns.web.status = 'succeeded'
    turns.web.result = { output: [{ kind: 'text', text: 'Canonical final answer' }, { kind: 'file', name: 'report.txt', source: { kind: 'asset', assetId: 'report' } }] }
    await nextTick()
    expect(wrapper.findAll('.turn-content > .markdown').map(item => item.text())).toEqual(['Before the tool', 'Canonical final answer'])
    expect(wrapper.text()).toContain('report.txt')
    expect(wrapper.find('.interaction-card').exists()).toBe(false)
    expect(wrapper.find('.thinking-dots').exists()).toBe(false)
  })
})

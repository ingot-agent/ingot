import type { InteractionField, Schema } from './protocol'

export function interactionValues(fields: InteractionField[], values: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const field of fields) {
    const value = values[field.name]
    if (value === undefined) {
      if (field.required && !field.hasDefault) throw new Error('requiredField')
      continue
    }
    if (field.kind === 'integer' || field.kind === 'number') {
      if (String(value).trim() === '') throw new Error(field.kind === 'integer' ? 'invalidInteger' : 'invalidNumber')
      const number = Number(value)
      if (!Number.isFinite(number)) throw new Error('invalidNumber')
      if (field.kind === 'integer' && !Number.isSafeInteger(number)) throw new Error('invalidInteger')
      output[field.name] = number
    } else output[field.name] = value
  }
  return output
}

// Only render complete, flat forms. All other schemas retain their JSON editor.
export function supportsForm(schema: Schema): boolean {
  if (schema.type !== 'object' || !schema.properties) return false
  const complex = ['$ref', '$dynamicRef', 'allOf', 'anyOf', 'oneOf', 'if', 'then', 'else', 'dependentSchemas', 'patternProperties']
  if (complex.some(key => key in schema)) return false
  return Object.values(schema.properties).every(field =>
    typeof field.type === 'string' && ['string', 'integer', 'number', 'boolean'].includes(field.type) &&
    !complex.some(key => key in field) &&
    (!field.enum || field.enum.every(value => ['string', 'number', 'boolean'].includes(typeof value))))
}

export function schemaFields(schema: Schema): InteractionField[] {
  return Object.entries(schema.properties || {}).map(([name, field]) => ({
    name, label: field.title || name, description: field.description,
    kind: field.type as InteractionField['kind'],
    required: schema.required?.includes(name) || false,
    sensitive: false, hasDefault: false,
  }))
}

export function parseObject(text: string): Record<string, unknown> {
  const value: unknown = JSON.parse(text)
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('jsonObject')
  return value as Record<string, unknown>
}

export function canRoundtripForm(text: string, schema: Schema): boolean {
  try {
    const value = parseObject(text)
    return Object.entries(value).every(([key, item]) => {
      const field = schema.properties?.[key]
      if (!field || item === null || typeof item === 'object') return false
      if (typeof item === 'number' && (!Number.isFinite(item) || (Number.isInteger(item) && !Number.isSafeInteger(item)))) return false
      return (field.type === 'integer' ? typeof item === 'number' && Number.isInteger(item) : typeof item === field.type) &&
        (!field.enum || field.enum.includes(item))
    })
  } catch { return false }
}

import * as Y from 'yjs'

export const MAX_TEXT = 1 << 20
export const MAX_STATE = 4 << 20
export const MAX_UPDATE = 256 << 10
export const MAX_VECTOR = 64 << 10

function validText(text) {
  if (typeof text !== 'string' || Buffer.byteLength(text, 'utf8') > MAX_TEXT ||
      text.includes(String.fromCharCode(0)) ||
      Buffer.from(text, 'utf8').toString('utf8') !== text) throw Error('invalid source')
  return text
}
function bytes(value, limit) {
  if (typeof value !== 'string' || !value.length || value.length > 4 * Math.ceil(limit / 3))
    throw Error('invalid binary size')
  const data = Buffer.from(value, 'base64')
  if (!data.length || data.length > limit || data.toString('base64') !== value)
    throw Error('invalid binary encoding')
  return data
}
// Do not allow unresolved dependencies into a committed checkpoint. Only
// the plain source Y.Text is supported, not maps, embeds or rich text.
function plainSource(doc) {
  if (doc.share.size !== 1 || !doc.share.has('source') ||
      doc.store.pendingStructs != null || doc.store.pendingDs != null)
    throw Error('unsupported or incomplete document')
  const source = doc.getText('source')
  const delta = source.toDelta()
  if (delta.some(part => typeof part.insert !== 'string' || part.attributes != null))
    throw Error('unsupported rich text')
  return validText(source.toString())
}
function checkpoint(doc) {
  const text = plainSource(doc)
  const state = Buffer.from(Y.encodeStateAsUpdate(doc))
  const vector = Buffer.from(Y.encodeStateVector(doc))
  if (state.length > MAX_STATE || vector.length > MAX_VECTOR) throw Error('checkpoint too large')
  return { text, state: state.toString('base64'), vector: vector.toString('base64') }
}

export function execute(request) {
  const doc = new Y.Doc({ gc: true })
  try {
    const text = validText(request.text)
    const source = doc.getText('source')
    if (request.op === 'seed') {
      if (text) source.insert(0, text)
      return checkpoint(doc)
    }
    if (request.op !== 'merge' && request.op !== 'read') throw Error('unknown operation')
    Y.applyUpdate(doc, bytes(request.state, MAX_STATE))
    if (plainSource(doc) !== text) throw Error('canonical text mismatch')
    if (request.op === 'merge') Y.applyUpdate(doc, bytes(request.update, MAX_UPDATE))
    return checkpoint(doc)
  } finally { doc.destroy() }
}

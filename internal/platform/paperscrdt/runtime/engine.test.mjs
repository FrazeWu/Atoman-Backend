import assert from 'node:assert/strict'
import test from 'node:test'
import * as Y from 'yjs'
import { execute, MAX_TEXT, MAX_UPDATE } from './engine.mjs'

const b64 = data => Buffer.from(data).toString('base64')
function peer(checkpoint) {
  const doc = new Y.Doc()
  const text = doc.getText('source')
  Y.applyUpdate(doc, Buffer.from(checkpoint.state, 'base64'))
  return { doc, text }
}
function update(checkpoint, edit) {
  const {doc,text} = peer(checkpoint)
  try {
    const before = Y.encodeStateVector(doc)
    edit(text,doc)
    return b64(Y.encodeStateAsUpdate(doc,before))
  } finally { doc.destroy() }
}
const merge = (base,change) => execute({op:'merge',...base,update:change})

test('independent clients converge, duplicate updates are idempotent, cold restart preserves Unicode',()=>{
  const seed = execute({op:'seed',text:'hello '+String.fromCodePoint(0x4e16,0x754c,0x1f600)})
  const a=update(seed,t=>t.insert(0,'A: '))
  const b=update(seed,t=>t.insert(t.length,' :B'))
  const ab=merge(merge(seed,a),b), ba=merge(merge(seed,b),a)
  assert.equal(ab.text,ba.text)
  assert.equal(ab.state,ba.state)
  assert.equal(ab.vector,ba.vector)
  assert.equal(ab.text,'A: '+seed.text+' :B')
  assert.deepEqual(merge(ab,a),ab)
  assert.deepEqual(execute({op:'read',...ab}),ab)
})
test('delete-only operations persist even when the state vector is unchanged',()=>{
  const seed=execute({op:'seed',text:'abc'})
  const deleted=merge(seed,update(seed,t=>t.delete(1,1)))
  assert.equal(deleted.text,'ac')
  assert.equal(deleted.vector,seed.vector)
  assert.notEqual(deleted.state,seed.state)
  assert.equal(execute({op:'read',...deleted}).text,'ac')
})
test('empty source, malformed updates, other shared types, embeds and canonical mismatch',()=>{
  const seed=execute({op:'seed',text:''})
  assert.equal(execute({op:'read',...seed}).text,'')
  assert.throws(()=>merge(seed,'not base64'))
  assert.throws(()=>merge(seed,b64([255,255,255])))
  assert.throws(()=>merge(seed,b64(Buffer.alloc(MAX_UPDATE+1))))
  assert.throws(()=>merge(seed,update(seed,(_,d)=>d.getMap('other').set('a',1))))
  assert.throws(()=>merge(seed,update(seed,t=>t.insertEmbed(0,{image:'x'}))))
  assert.throws(()=>merge(seed,update(seed,t=>t.insert(0,'x',{bold:true}))))
  assert.throws(()=>execute({op:'read',...seed,text:'wrong'}))
  assert.throws(()=>execute({op:'seed',text:'x'.repeat(MAX_TEXT+1)}))
  assert.throws(()=>execute({op:'seed',text:String.fromCharCode(0xd800)}))
})
test('rejects missing causal predecessor; retry after predecessor is accepted',()=>{
  const seed=execute({op:'seed',text:'start'})
  const {doc,text}=peer(seed)
  try {
    const v0=Y.encodeStateVector(doc)
    text.insert(text.length,' first')
    const first=b64(Y.encodeStateAsUpdate(doc,v0))
    const v1=Y.encodeStateVector(doc)
    text.insert(text.length,' second')
    const second=b64(Y.encodeStateAsUpdate(doc,v1))
    assert.throws(()=>merge(seed,second))
    assert.equal(merge(merge(seed,first),second).text,'start first second')
  } finally {doc.destroy()}
})

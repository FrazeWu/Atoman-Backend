// Test helper only. Simulates an independent browser Y.Doc and sends a delta.
import * as Y from 'yjs'
const chunks=[]
for await (const chunk of process.stdin) chunks.push(chunk)
const input=JSON.parse(Buffer.concat(chunks).toString('utf8'))
const doc=new Y.Doc()
try {
 const source=doc.getText('source')
 Y.applyUpdate(doc,Buffer.from(input.state,'base64'))
 const vector=Y.encodeStateVector(doc)
 if(input.remove)source.delete(input.position,input.remove)
 if(input.insert)source.insert(input.position,input.insert)
 process.stdout.write(Buffer.from(Y.encodeStateAsUpdate(doc,vector)).toString('base64'))
} finally {doc.destroy()}

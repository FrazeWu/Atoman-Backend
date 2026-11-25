import { execute } from './engine.mjs'
const maxInput = 13 << 20
let size = 0
const chunks = []
try {
  for await (const chunk of process.stdin) {
    size += chunk.length
    if (size > maxInput) throw Error('input too large')
    chunks.push(chunk)
  }
  const request = JSON.parse(Buffer.concat(chunks).toString('utf8'))
  const result = execute(request)
  process.stdout.write(JSON.stringify(result))
} catch {
  // Never log manuscript contents or untrusted decoder exception text.
  process.stderr.write('CRDT checkpoint rejected\n')
  process.exitCode = 2
}

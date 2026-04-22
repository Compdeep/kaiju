import assert from 'node:assert/strict'
import { square, cube } from './math.mjs'

assert.equal(square(0), 0)
assert.equal(square(3), 9)
assert.equal(square(-4), 16)

assert.equal(cube(0), 0)
assert.equal(cube(2), 8)
assert.equal(cube(3), 27)
assert.equal(cube(-2), -8)

console.log('ok')

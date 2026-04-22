import { Router } from 'express'

const router = Router()
const users = new Map()

router.get('/users', (req, res) => {
  res.json(Array.from(users.values()))
})

router.get('/users/:id', (req, res) => {
  const u = users.get(req.params.id)
  if (!u) return res.status(404).json({ error: 'not found' })
  res.json(u)
})

router.post('/users', (req, res) => {
  const { id, email, name } = req.body || {}
  if (!id || !email) return res.status(400).json({ error: 'id and email required' })
  users.set(id, { id, email, name: name || '' })
  res.status(201).json(users.get(id))
})

export { router }

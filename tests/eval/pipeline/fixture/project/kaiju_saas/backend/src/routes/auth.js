import express from 'express'

const router = express.Router()

router.post('/register', (req, res) => {
  res.json({ ok: true, endpoint: 'register' })
})

router.post('/login', (req, res) => {
  res.json({ ok: true, endpoint: 'login' })
})

export { router }

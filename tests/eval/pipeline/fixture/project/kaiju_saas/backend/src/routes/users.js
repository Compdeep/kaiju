import express from 'express'

const router = express.Router()

router.get('/me', (req, res) => {
  res.json({ id: 1, email: 'placeholder@example.com' })
})

export { router }

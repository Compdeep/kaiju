import express from 'express'

const router = express.Router()

router.post('/create-intent', (req, res) => {
  res.json({ clientSecret: 'pi_placeholder_secret' })
})

export { router }

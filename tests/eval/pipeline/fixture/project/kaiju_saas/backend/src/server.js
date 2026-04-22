import express from 'express'
import authRouter from './routes/auth.js'
import usersRouter from './routes/users.js'
import paymentsRouter from './routes/payments.js'

const app = express()
app.use(express.json())

app.get('/health', (_req, res) => res.json({ status: 'ok' }))

app.use('/auth', authRouter)
app.use('/users', usersRouter)
app.use('/payments', paymentsRouter)

const PORT = process.env.PORT || 4000
app.listen(PORT, () => {
  console.log(`backend listening on ${PORT}`)
})

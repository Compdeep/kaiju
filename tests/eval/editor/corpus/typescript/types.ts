export interface User {
  id: number
  email: string
  name: string
  createdAt: string
}

export interface Session {
  token: string
  userId: number
  expiresAt: string
}

export type AuthState =
  | { status: 'anon' }
  | { status: 'authenticated'; user: User; session: Session }
  | { status: 'error'; error: string }

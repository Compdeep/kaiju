export interface User {
  id: number
  email: string
  name: string
}

export class UserService {
  private baseUrl: string

  constructor(baseUrl: string) {
    this.baseUrl = baseUrl
  }

  get(id: number): Promise<User> {
    return fetch(`${this.baseUrl}/users/${id}`).then((r) => {
      if (!r.ok) throw new Error(`GET /users/${id} failed: ${r.status}`)
      return r.json() as Promise<User>
    })
  }

  list(): Promise<User[]> {
    return fetch(`${this.baseUrl}/users`).then((r) => r.json() as Promise<User[]>)
  }
}

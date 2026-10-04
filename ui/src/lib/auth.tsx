import { createContext, useContext } from 'react'

export type Role = 'viewer' | 'operator' | 'admin'

export interface User {
  sub: string
  name: string
  email?: string
  role: Role
  provider: 'local' | 'entra' | 'anonymous'
}

const rank: Record<Role, number> = { viewer: 1, operator: 2, admin: 3 }

export const AuthContext = createContext<User | null>(null)

// useAuth exposes the signed-in user and a role check. The server enforces every role on
// its own; the UI only hides actions the user could not perform anyway.
export function useAuth() {
  const user = useContext(AuthContext)
  return {
    user,
    can: (role: Role) => !!user && rank[user.role] >= rank[role],
  }
}

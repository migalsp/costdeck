import { createContext, useContext } from 'react'

export type Role = 'viewer' | 'operator' | 'admin'

export interface User {
  sub: string
  name: string
  email?: string
  role: Role
  provider: 'local' | 'entra' | 'token' | 'anonymous'
  // Set for a local user whose password an administrator chose.
  mustChangePassword?: boolean
}

// Local users managed under Settings → Users sign in as user:<name>; the built-in admin
// as local:<name>. Only managed users change their own password in the dashboard.
export const isManagedUser = (user: User | null) => !!user && user.sub.startsWith('user:')

export const roleHelp: Record<Role, string> = {
  viewer: 'Sees every page and report; changes nothing',
  operator: 'Also starts and stops schedules and generates AI reports',
  admin: 'Also edits schedules, settings, users and API tokens',
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

import { cn } from '../lib/utils'
import './user-identity.css'

export type UserIdentityButtonProps = {
  userId: number
  username?: string
  displayName?: string
  group?: string
  email?: string
  onClick: () => void
  compact?: boolean
  className?: string
  disabled?: boolean
  showId?: boolean
}

export function UserIdentityButton({
  userId, username, displayName, group, email, onClick,
  compact = false, className, disabled = false, showId = true,
}: UserIdentityButtonProps) {
  const name = displayName || username || `用户 #${userId}`
  const secondary = username && username !== name ? username : ''
  const avatar = Array.from(name.trim())[0]?.toLocaleUpperCase() || '#'
  return <button
    type="button"
    className={cn('user-identity', compact && 'user-identity--compact', className)}
    onClick={onClick}
    disabled={disabled}
    aria-label={`查看 ${name}（用户 #${userId}）的详情`}
    aria-haspopup="dialog"
    title={[name, secondary, `ID: ${userId}`, group].filter(Boolean).join(' · ')}
  >
    <span className="user-identity__avatar" aria-hidden="true">{avatar}</span>
    <span className="user-identity__copy">
      <span className="user-identity__name">{name}</span>
      <span className="user-identity__meta">
        {secondary && <span className="user-identity__username">{secondary}</span>}
        {group && <span className="user-identity__group">{group}</span>}
        {(showId || !secondary && !group) && <span className="user-identity__id">#{userId}</span>}
      </span>
      {!compact && email && <span className="user-identity__email">{email}</span>}
    </span>
  </button>
}

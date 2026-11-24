import type { ReactNode } from 'react'
import { cn } from '../lib/utils'
import './scroll-region.css'

export type ScrollRegionProps = {
  label: string
  size?: 'sm' | 'md' | 'lg'
  fixed?: boolean
  className?: string
  children: ReactNode
}

export function ScrollRegion({ label, size = 'md', fixed = false, className, children }: ScrollRegionProps) {
  return <div
    className={cn('scroll-region', `scroll-region--${size}`, fixed && 'scroll-region--fixed', className)}
    role="region"
    aria-label={label}
    tabIndex={0}
  >{children}</div>
}

import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { ComponentType, ReactNode } from 'react'
import * as DialogPrimitive from '@radix-ui/react-dialog'
import { LayoutDashboard, Loader2, LogOut, Menu, Moon, Settings, Sun, UserRound, X } from 'lucide-react'
import { Badge } from './components/ui/badge'
import { Button } from './components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from './components/ui/dialog'
import { cn } from './lib/utils'
import { UI_PREVIEW_ONLY } from './api'
import type { OwnerIdentity } from './types'

export type WorkspaceTab = { path: string; title: string; icon: ComponentType<{ className?: string }> }
type Theme = 'light' | 'dark' | 'system'
const hiddenKey = 'siteadmin-tools-hidden-tabs'

function hiddenFeatures() {
  try {
    const saved = JSON.parse(localStorage.getItem(hiddenKey) || '[]')
    return new Set<string>(Array.isArray(saved) ? saved.filter(item => typeof item === 'string' && item !== '/') : [])
  } catch { return new Set<string>() }
}

function SwitchMark({ enabled }: { enabled: boolean }) {
  return <span className={cn('relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors', enabled ? 'bg-primary' : 'bg-muted-foreground/30')}>
    <span className={cn('absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-all', enabled ? 'left-[18px]' : 'left-0.5')} />
  </span>
}

export default function ToolsLayout({ children, tabs, currentPath, onNavigate, owner, logoutBusy, onLogout }: {
  children: ReactNode; tabs: WorkspaceTab[]; currentPath: string
  onNavigate: (path: string) => void; owner?: OwnerIdentity; logoutBusy?: boolean; onLogout: () => void
}) {
  const [mobileOpen, setMobileOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [hidden, setHidden] = useState(hiddenFeatures)
  const [theme, setTheme] = useState<Theme>(() => {
    const stored = localStorage.getItem('siteadmin-tools-theme')
    return stored === 'dark' || stored === 'system' ? stored : 'light'
  })
  const [indicator, setIndicator] = useState({ left: 0, width: 0, opacity: 0 })
  const navigation = useRef<HTMLElement>(null)
  const buttons = useRef<(HTMLButtonElement | null)[]>([])
  const visibleTabs = tabs.filter(tab => tab.path === '/' || !hidden.has(tab.path))
  const activeTitle = tabs.find(tab => tab.path === currentPath)?.title ?? '仪表板'

  useEffect(() => {
    localStorage.setItem(hiddenKey, JSON.stringify([...hidden]))
    if (hidden.has(currentPath) && currentPath !== '/') onNavigate('/')
  }, [hidden, currentPath, onNavigate])
  useEffect(() => { setMobileOpen(false) }, [currentPath])
  useEffect(() => {
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const apply = () => document.documentElement.classList.toggle('dark', theme === 'dark' || theme === 'system' && media.matches)
    apply()
    localStorage.setItem('siteadmin-tools-theme', theme)
    media.addEventListener('change', apply)
    return () => media.removeEventListener('change', apply)
  }, [theme])

  useLayoutEffect(() => {
    const updateNavigation = () => {
      const nav = navigation.current
      if (!nav || !nav.clientWidth) return
      const index = visibleTabs.findIndex(tab => tab.path === currentPath)
      const selected = buttons.current[index]
      if (!selected) return
      setIndicator({ left: selected.offsetLeft, width: selected.offsetWidth, opacity: 1 })
      const start = selected.offsetLeft
      const end = start + selected.offsetWidth
      if (start < nav.scrollLeft) nav.scrollLeft = start
      else if (end > nav.scrollLeft + nav.clientWidth) nav.scrollLeft = end - nav.clientWidth
    }
    updateNavigation()
    const observer = new ResizeObserver(updateNavigation)
    if (navigation.current) observer.observe(navigation.current)
    window.addEventListener('resize', updateNavigation)
    return () => { observer.disconnect(); window.removeEventListener('resize', updateNavigation) }
  }, [currentPath, hidden, tabs])

  function toggle(path: string) {
    setHidden(previous => {
      const next = new Set(previous)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  return <div className="tools-shell flex min-h-screen min-w-0 flex-col bg-background">
    <div className="sticky top-0 z-40 w-full border-b border-border/40 bg-background/60 shadow-sm backdrop-blur-xl transition-colors duration-300 supports-[backdrop-filter]:bg-background/40 dark:shadow-none">
      <header className="w-full">
        <div className="tools-container">
          <div className="flex items-center justify-between gap-3 py-3">
            <div className="flex min-w-0 items-center gap-3">
              <Button variant="ghost" size="sm" onClick={() => setMobileOpen(true)} className="-ml-2 h-9 w-9 px-0 md:hidden" aria-label="打开导航"><Menu className="h-5 w-5" /></Button>
              <button type="button" className="flex min-w-0 items-center gap-2 text-left" onClick={() => onNavigate('/')} aria-label="站长后台首页">
                <img src="/tool.svg" alt="" className="h-8 w-8 shrink-0" />
                <span className="truncate text-lg font-bold text-foreground sm:text-xl"><span className="md:hidden">{activeTitle}</span><span className="hidden md:inline">站长后台</span></span>
              </button>
              <Badge variant="outline" className="hidden h-6 items-center gap-1.5 px-2 py-0.5 md:flex">
                <span className={cn('h-1.5 w-1.5 rounded-full', UI_PREVIEW_ONLY ? 'bg-amber-500' : 'bg-primary')} />
                <span className="text-[10px] font-medium">{UI_PREVIEW_ONLY ? '数据获取已暂停' : 'NewAPI'}</span>
              </Badge>
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              {owner && <div className="mr-1 hidden min-w-0 items-center gap-2 rounded-full border border-border/60 bg-card/70 py-1 pl-1 pr-3 sm:flex" title={`站长账号：${owner.username}`}><span className="flex h-7 w-7 items-center justify-center rounded-full bg-primary/10 text-primary"><UserRound className="h-3.5 w-3.5" /></span><span className="max-w-36 truncate text-xs font-medium">{owner.display_name || owner.username}</span></div>}
              <Button variant="ghost" size="sm" onClick={() => setSettingsOpen(true)} title="功能设置" aria-label="功能设置" className="text-muted-foreground hover:bg-muted/50 hover:text-foreground"><Settings className="h-4 w-4" /></Button>
              <Button type="button" variant="ghost" size="sm" onClick={onLogout} disabled={logoutBusy} title="退出登录" aria-label="退出登录" className="text-muted-foreground hover:bg-muted/50 hover:text-foreground">{logoutBusy ? <Loader2 className="h-4 w-4 animate-spin sm:mr-2" /> : <LogOut className="h-4 w-4 sm:mr-2" />}<span className="hidden sm:inline">{logoutBusy ? '退出中' : '退出'}</span></Button>
            </div>
          </div>
        </div>
      </header>
      <div className="hidden w-full border-t border-border/40 md:block">
        <div className="tools-container">
          <nav ref={navigation} className="tools-navigation custom-scrollbar relative flex w-full items-center overflow-x-auto" aria-label="功能导航">
            <div className="pointer-events-none absolute inset-y-2 rounded-md border border-border/50 bg-secondary shadow-sm transition-all duration-300 ease-out" style={indicator} />
            {visibleTabs.map((tab, index) => {
              const Icon = tab.icon
              return <button key={tab.path} ref={element => { buttons.current[index] = element }} type="button" onClick={() => onNavigate(tab.path)} aria-current={currentPath === tab.path ? 'page' : undefined}
                className={cn('tools-navigation-tab relative z-10 flex select-none items-center justify-center whitespace-nowrap rounded-md font-medium outline-none transition-colors duration-200 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
                  currentPath === tab.path ? 'text-foreground drop-shadow-sm' : 'text-muted-foreground hover:text-foreground/80')}>
                <Icon className={cn('h-[1.15em] w-[1.15em] shrink-0 transition-transform duration-300', currentPath === tab.path ? 'scale-110 text-primary' : 'scale-100')} /><span>{tab.title}</span>
              </button>
            })}
          </nav>
        </div>
      </div>
    </div>

    <DialogPrimitive.Root open={mobileOpen} onOpenChange={setMobileOpen}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-[60] bg-black/50 backdrop-blur-sm md:hidden" />
        <DialogPrimitive.Content className="fixed inset-y-0 left-0 z-[61] flex w-[78%] max-w-xs flex-col border-r border-border bg-background shadow-2xl md:hidden" aria-describedby={undefined}>
          <div className="flex h-14 shrink-0 items-center justify-between border-b border-border/60 px-4">
            <div className="flex items-center gap-2"><img src="/tool.svg" alt="" className="h-6 w-6" /><DialogPrimitive.Title className="font-semibold">站长后台</DialogPrimitive.Title></div>
            <DialogPrimitive.Close asChild><Button variant="ghost" size="sm" className="h-8 w-8 px-0" aria-label="关闭导航"><X className="h-5 w-5" /></Button></DialogPrimitive.Close>
          </div>
          <nav className="flex-1 overflow-y-auto py-2" aria-label="手机功能导航">{visibleTabs.map(tab => {
            const Icon = tab.icon
            return <button key={tab.path} type="button" onClick={() => { onNavigate(tab.path); setMobileOpen(false) }} aria-current={currentPath === tab.path ? 'page' : undefined}
              className={cn('flex w-full items-center gap-3 px-4 py-3 text-sm font-medium transition-colors', currentPath === tab.path ? 'bg-secondary text-foreground' : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground')}>
              <Icon className={cn('h-5 w-5 shrink-0', currentPath === tab.path && 'text-primary')} /><span className="truncate">{tab.title}</span>
            </button>
          })}</nav>
          <div className="border-t border-border/60 px-4 py-3 text-xs text-muted-foreground"><div className="flex items-center gap-2"><span className={cn('h-2 w-2 rounded-full', UI_PREVIEW_ONLY ? 'bg-amber-500' : 'bg-primary')} />{UI_PREVIEW_ONLY ? '数据获取已暂停' : owner ? `站长 · ${owner.display_name || owner.username}` : 'NewAPI 站长后台'}</div></div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>

    <Dialog open={settingsOpen} onOpenChange={setSettingsOpen}>
      <DialogContent className="max-w-md">
        <DialogHeader><DialogTitle className="flex items-center gap-2"><Settings className="h-5 w-5" />功能设置</DialogTitle><DialogDescription>关闭暂时用不到的功能页，导航栏不再显示（配置保存在本浏览器）。</DialogDescription></DialogHeader>
        <div className="-mx-2 mb-1 border-b border-border pb-1">
          <button type="button" onClick={() => setHidden(hidden.size ? new Set() : new Set(tabs.filter(tab => tab.path !== '/').map(tab => tab.path)))} role="switch" aria-checked={hidden.size === 0}
            className="flex w-full items-center gap-3 rounded-md px-2 py-2.5 text-sm font-semibold transition-colors hover:bg-muted/40">
            <LayoutDashboard className={cn('h-4 w-4 shrink-0', hidden.size === 0 ? 'text-primary' : 'text-muted-foreground/50')} /><span className="flex-1 text-left">显示全部</span><SwitchMark enabled={hidden.size === 0} />
          </button>
        </div>
        <div className="-mx-2 divide-y divide-border/60">{tabs.filter(tab => tab.path !== '/').map(tab => {
          const enabled = !hidden.has(tab.path), Icon = tab.icon
          return <button key={tab.path} type="button" onClick={() => toggle(tab.path)} role="switch" aria-checked={enabled}
            className="flex w-full items-center gap-3 rounded-md px-2 py-2.5 text-sm transition-colors hover:bg-muted/40">
            <Icon className={cn('h-4 w-4 shrink-0', enabled ? 'text-primary' : 'text-muted-foreground/50')} /><span className={cn('flex-1 text-left', !enabled && 'text-muted-foreground line-through')}>{tab.title}</span><SwitchMark enabled={enabled} />
          </button>
        })}</div>
        <p className="text-xs text-muted-foreground">仪表板为默认页，不可关闭。</p>
        <div className="flex items-center justify-between gap-3 border-t pt-4"><span className="text-sm text-muted-foreground">外观</span><div className="flex rounded-md bg-muted p-1">{([{ value: 'light', label: '浅色', icon: Sun }, { value: 'dark', label: '深色', icon: Moon }] as const).map(item => <Button key={item.value} variant={theme === item.value ? 'secondary' : 'ghost'} size="sm" onClick={() => setTheme(item.value)} aria-pressed={theme === item.value} className="h-8 gap-1.5"><item.icon className="h-3.5 w-3.5" />{item.label}</Button>)}</div></div>
      </DialogContent>
    </Dialog>

    <main className="tools-container tools-main min-w-0 flex-1">{children}</main>
  </div>
}

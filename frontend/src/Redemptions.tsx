// Adapted from new_api_tools (RedemptionCenter.tsx), MIT License; see tools-reference-LICENSE.txt.
import { useEffect, useState } from 'react'
import { Ticket, Plus, Clock } from 'lucide-react'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './components/ui/tabs'
import RedemptionGenerator from './RedemptionGenerator'
import { RedemptionManagement } from './RedemptionManagement'
import { RedemptionHistory } from './RedemptionHistory'

type View = 'manage' | 'generator' | 'history'

const validViews: View[] = ['manage', 'generator', 'history']

function getInitialView(): View {
  const view = new URLSearchParams(window.location.search).get('view')
  if (view && (validViews as string[]).includes(view)) {
    return view as View
  }
  return 'manage'
}

function syncViewToURL(view: View) {
  const params = new URLSearchParams(window.location.search)
  if (view === 'manage') {
    params.delete('view')
  } else {
    params.set('view', view)
  }
  const query = params.toString()
  const next = `${window.location.pathname}${query ? `?${query}` : ''}`
  window.history.replaceState(null, '', next)
}

export type RedemptionCenterProps = {
  tokenVersion?: number
  quotaPerUnit?: number
  timezone?: string
  demoMode?: boolean
  onUnauthorized: () => void
}

export default function Redemptions(props: RedemptionCenterProps) {
  const [view, setView] = useState<View>(() => getInitialView())

  useEffect(() => {
    syncViewToURL(view)
  }, [view])

  return (
    <Tabs value={view} onValueChange={(value) => setView(value as View)} className="space-y-4">
      <TabsList className="grid w-full grid-cols-3 md:w-auto md:inline-flex">
        <TabsTrigger value="manage" className="gap-2">
          <Ticket className="h-4 w-4" />
          兑换码
        </TabsTrigger>
        <TabsTrigger value="generator" className="gap-2">
          <Plus className="h-4 w-4" />
          生成器
        </TabsTrigger>
        <TabsTrigger value="history" className="gap-2">
          <Clock className="h-4 w-4" />
          生成记录
        </TabsTrigger>
      </TabsList>

      <TabsContent value="manage" className="mt-0">
        <RedemptionManagement {...props} />
      </TabsContent>
      <TabsContent value="generator" className="mt-0">
        <RedemptionGenerator {...props} />
      </TabsContent>
      <TabsContent value="history" className="mt-0">
        <RedemptionHistory />
      </TabsContent>
    </Tabs>
  )
}

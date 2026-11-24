import { useEffect, useMemo, useState } from 'react'
import ReactECharts from 'echarts-for-react/lib/core'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { Activity, ChartNoAxesCombined, Loader2 } from 'lucide-react'
import { Card, CardContent, CardHeader, CardTitle } from './components/ui/card'

echarts.use([LineChart, GridComponent, TooltipComponent, CanvasRenderer])

type TrendView = 'count' | 'rate'
type TrendCounts = {
  success_count: number
  failure_count: number
  empty_count: number | null
  success_rate: number | null
  empty_rate: number | null
}
type TrendData = {
  period: '24h' | '7d' | '30d'
  timezone: string
  summary: TrendCounts & { total_requests: number }
  trend: (TrendCounts & { at: string })[]
}
const periodLabels = { '24h': '最近 24 小时', '7d': '近 7 天', '30d': '近 30 天' }
const colors = { success: '#3b82f6', failure: '#f43f5e', empty: '#f59e0b' }
const numberFormat = new Intl.NumberFormat('zh-CN')

function format(value: number | null | undefined, percent = false) {
  if (value == null || !Number.isFinite(value)) return '—'
  return percent ? `${value.toFixed(1)}%` : numberFormat.format(value)
}

export default function ModelStatusTrend({ data, loading, emptyMessage }: {
  data: TrendData | null
  loading: boolean
  emptyMessage: string
}) {
  const [view, setView] = useState<TrendView>('count')
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))

  useEffect(() => {
    const observer = new MutationObserver(() => setDark(document.documentElement.classList.contains('dark')))
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [])

  const series = view === 'count' ? [
    { name: '成功数', color: colors.success, value: data?.summary.success_count, key: 'success_count' as const },
    { name: '失败数', color: colors.failure, value: data?.summary.failure_count, key: 'failure_count' as const },
    { name: '疑似空回复', color: colors.empty, value: data?.summary.empty_count, key: 'empty_count' as const },
  ] : [
    { name: '成功率', color: colors.success, value: data?.summary.success_rate, key: 'success_rate' as const },
    { name: '空回复率', color: colors.empty, value: data?.summary.empty_rate, key: 'empty_rate' as const },
  ]
  const option = useMemo(() => {
    if (!data) return {}
    const rateView = view === 'rate'
    const tickFormat = new Intl.DateTimeFormat('zh-CN', data.period === '24h'
      ? { timeZone: data.timezone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }
      : { timeZone: data.timezone, month: 'numeric', day: 'numeric' })
    const tooltipFormat = new Intl.DateTimeFormat('zh-CN', {
      timeZone: data.timezone, month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
    })
    const lines = rateView ? [
      { name: '成功率', key: 'success_rate' as const, color: colors.success, fill: true },
      { name: '空回复率', key: 'empty_rate' as const, color: colors.empty, fill: false },
    ] : [
      { name: '成功数', key: 'success_count' as const, color: colors.success, fill: true },
      { name: '失败数', key: 'failure_count' as const, color: colors.failure, fill: false },
      { name: '疑似空回复', key: 'empty_count' as const, color: colors.empty, fill: false },
    ]
    return {
      animation: false,
      backgroundColor: 'transparent',
      grid: { top: 36, left: 14, right: 24, bottom: 26, containLabel: true },
      tooltip: {
        trigger: 'axis', renderMode: 'richText', confine: true,
        backgroundColor: dark ? '#172033' : '#ffffff',
        borderColor: dark ? '#334155' : '#e2e8f0',
        borderWidth: 1, padding: [12, 16],
        textStyle: { color: dark ? '#e2e8f0' : '#334155', fontSize: 12, lineHeight: 24 },
        axisPointer: {
          type: 'line', axis: 'x',
          lineStyle: { color: dark ? '#64748b' : '#94a3b8', type: 'dashed', width: 1 },
          label: {
            show: true, backgroundColor: dark ? '#334155' : '#475569', padding: [6, 9],
            formatter: ({ value }: { value: string | number }) => tooltipFormat.format(new Date(String(value))),
          },
        },
        valueFormatter: (value: unknown) => typeof value === 'number' ? format(value, rateView) : '—',
      },
      xAxis: {
        type: 'category', boundaryGap: false,
        data: data.trend.map(point => point.at),
        axisTick: { show: false }, axisLine: { show: false },
        axisLabel: {
          color: dark ? '#94a3b8' : '#64748b', fontSize: 12, margin: 18,
          hideOverlap: true, showMinLabel: true, showMaxLabel: true,
          formatter: (value: string) => tickFormat.format(new Date(value)),
        },
        axisPointer: { snap: true },
      },
      yAxis: {
        type: 'value', min: 0, ...(rateView ? { max: 100, interval: 25 } : { minInterval: 1, splitNumber: 4 }),
        axisLine: { show: false }, axisTick: { show: false },
        axisLabel: {
          color: dark ? '#94a3b8' : '#64748b', fontSize: 12, margin: 12,
          formatter: (value: number) => rateView ? `${value}%` : numberFormat.format(value),
        },
        splitLine: { lineStyle: { color: dark ? '#263348' : '#e8edf5', type: 'dashed' } },
      },
      series: lines.map((line, index) => ({
        name: line.name, type: 'line', smooth: .18, smoothMonotone: 'x', connectNulls: false,
        showSymbol: false, symbol: 'circle', symbolSize: 7,
        itemStyle: { color: line.color, borderColor: dark ? '#172033' : '#ffffff', borderWidth: 2 },
        emphasis: { focus: 'series', lineStyle: { width: 3 }, scale: 1.4 },
        ...(line.fill ? { areaStyle: {
          color: {
            type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [{ offset: 0, color: dark ? 'rgba(59,130,246,.24)' : 'rgba(59,130,246,.15)' },
              { offset: 1, color: 'rgba(59,130,246,0)' }],
          },
        } } : {}),
        lineStyle: { color: line.color, width: line.key === 'success_count' || line.key === 'success_rate' ? 3 : 2.2, type: line.key.startsWith('empty') ? 'dashed' : 'solid' },
        markLine: index === 0 && view === 'rate' ? { silent: true, symbol: 'none', data: [{ yAxis: 100 }], lineStyle: { color: dark ? '#334155' : '#cbd5e1', type: 'dashed', width: 1 }, label: { show: false } } : undefined,
      })),
    }
  }, [data, view, dark])

  return <Card className="model-status-trend" aria-labelledby="model-trend-title">
    <CardHeader className="model-status-trend__header">
      <div className="model-status-trend__heading">
        <span className="model-status-trend__icon" aria-hidden="true"><ChartNoAxesCombined className="h-5 w-5" /></span>
        <div><CardTitle id="model-trend-title" className="text-lg">模型状态趋势</CardTitle>
          <p>{data ? periodLabels[data.period] : '当前统计范围'} · {view === 'count' ? '观察调用量与异常变化' : '观察成功率与空回复率'}</p></div>
      </div>
      <div className="model-status-trend__switch" role="group" aria-label="趋势展示方式">
        <button type="button" aria-pressed={view === 'count'} onClick={() => setView('count')}>请求量</button>
        <button type="button" aria-pressed={view === 'rate'} onClick={() => setView('rate')}>成功率 / 空回复率</button>
      </div>
    </CardHeader>
    <CardContent className="model-status-trend__content">
      <div className="model-status-trend__legend" aria-label="当前范围汇总">
        {series.map(line => <div key={line.key}>
          <i style={{ backgroundColor: line.color }} aria-hidden="true" /><span>{line.name}</span>
          <strong style={{ color: line.color }}>{format(line.value, view === 'rate')}</strong>
        </div>)}
      </div>
      <div className="model-status-trend__plot" role="img"
        aria-label={view === 'count' ? '模型成功、失败和疑似空回复数量趋势，空回复包含在成功数中' : '模型成功率和空回复率趋势，纵轴范围为百分之零至百分之一百'}
        aria-busy={loading}>
        {data && data.summary.total_requests > 0
          ? <ReactECharts echarts={echarts} option={option} style={{ height: '100%', width: '100%' }} notMerge />
          : <div className="model-status-trend__empty" role="status">
            {loading ? <Loader2 className="h-8 w-8 animate-spin" /> : <Activity className="h-8 w-8" />}<span>{emptyMessage}</span>
          </div>}
      </div>
      <p className="model-status-trend__footnote">{view === 'count'
        ? '疑似空回复已包含在成功数中，曲线不累加。'
        : '成功率 = 成功数 / 总请求；空回复率 = 空回复数 / 成功数。缺失数据保持断线。'}</p>
    </CardContent>
  </Card>
}

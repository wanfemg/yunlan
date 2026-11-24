import ReactECharts from 'echarts-for-react/lib/core'
import * as echarts from 'echarts/core'
import { BarChart, LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([BarChart, LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

export default function DashboardChart({ option }: { option: object }) {
  return <ReactECharts echarts={echarts} option={option} style={{ height: '100%', width: '100%' }} notMerge lazyUpdate />
}

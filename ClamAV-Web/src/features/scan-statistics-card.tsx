import * as React from "react"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"

import { ErrorAlert } from "@/components/error-alert"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  type ChartConfig,
} from "@/components/ui/chart"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { api, sleep } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type {
  StatisticsCounts,
  StatisticsLookupResponse,
  StatisticsLookupStart,
} from "@/lib/types"

const defaultScope = 7
const pollInterval = 500
const debounceDelay = 1000

const chartConfig = {
  clean: {
    label: "未发现威胁",
    color: "var(--success)",
  },
  found: {
    label: "发现威胁",
    color: "var(--destructive)",
  },
  error: {
    label: "扫描失败",
    color: "var(--chart-4)",
  },
  unknown: {
    label: "未知 / 运行中",
    color: "var(--muted-foreground)",
  },
} satisfies ChartConfig

type ChartPoint = StatisticsCounts & {
  date: string
  label: string
  total: number
}

type StatisticsResult = keyof StatisticsCounts

type HoveredSegment = {
  date: string
  result: StatisticsResult
  value: number
}

function StatisticsTooltip({
  active,
  segment,
}: {
  active?: boolean
  segment: HoveredSegment | null
}) {
  if (!active || !segment) return null

  const config = chartConfig[segment.result]

  return (
    <div className="grid min-w-32 gap-1.5 rounded-lg border border-border/50 bg-background px-2.5 py-1.5 text-xs shadow-xl">
      <div className="font-medium">{segment.date}</div>
      <div className="flex items-center gap-2">
        <div
          className="size-2.5 shrink-0 rounded-[2px]"
          style={{ backgroundColor: config.color }}
        />
        <span className="flex-1 text-muted-foreground">{config.label}</span>
        <span className="font-mono font-medium tabular-nums">
          {segment.value.toLocaleString()}
        </span>
      </div>
    </div>
  )
}

function normalizeScope(value: string) {
  const trimmed = value.trim()
  if (!/^[+-]?(?:\d+\.?\d*|\.\d+)$/.test(trimmed)) return null

  const parsed = Number(trimmed)
  if (!Number.isFinite(parsed)) return null
  if (parsed < 1) return 1
  if (parsed > 31) return 31
  if (!Number.isInteger(parsed)) return null
  return parsed
}

function isCounts(value: unknown): value is StatisticsCounts {
  if (typeof value !== "object" || value === null) return false
  return ["unknown", "clean", "found", "error"].every(
    (key) =>
      key in value &&
      typeof value[key as keyof typeof value] === "number" &&
      Number.isFinite(value[key as keyof typeof value])
  )
}

function toChartData(response: StatisticsLookupResponse): ChartPoint[] {
  return Object.entries(response)
    .filter(([key, value]) => /^\d{8}$/.test(key) && isCounts(value))
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([date, value]) => {
      const counts = value as StatisticsCounts
      return {
        date,
        label: `${date.slice(4, 6)}-${date.slice(6, 8)}`,
        ...counts,
        total: counts.unknown + counts.clean + counts.found + counts.error,
      }
    })
}

export function ScanStatisticsCard() {
  const [scopeInput, setScopeInput] = React.useState(String(defaultScope))
  const [data, setData] = React.useState<ChartPoint[]>([])
  const [total, setTotal] = React.useState<number | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState("")
  const [inputTouched, setInputTouched] = React.useState(false)
  const [hoveredSegment, setHoveredSegment] =
    React.useState<HoveredSegment | null>(null)
  const requestVersion = React.useRef(0)
  const debounceTimer = React.useRef<number | null>(null)
  const skipNextDebounce = React.useRef(false)

  const load = React.useCallback(async (scope: number) => {
    const version = ++requestVersion.current
    setLoading(true)
    setError("")
    setData([])
    setTotal(null)

    try {
      const start = await api<StatisticsLookupStart>(
        `/api/results/statistics/lookups?scope=${scope}`,
        { method: "POST" }
      )
      let response = await api<StatisticsLookupResponse>(
        `/api/results/statistics/lookups/${encodeURIComponent(start.lookup_id)}`
      )

      while (response.status === "pending") {
        if (version !== requestVersion.current) return
        await sleep(pollInterval)
        if (version !== requestVersion.current) return
        response = await api<StatisticsLookupResponse>(
          `/api/results/statistics/lookups/${encodeURIComponent(start.lookup_id)}`
        )
      }

      if (response.status === "failed") {
        throw new Error(response.error || "扫描统计加载失败")
      }
      if (version !== requestVersion.current) return

      setData(toChartData(response))
      setTotal(response.total)
    } catch (nextError) {
      if (version === requestVersion.current) {
        setError(errorMessage(nextError))
      }
    } finally {
      if (version === requestVersion.current) {
        setLoading(false)
      }
    }
  }, [])

  const requestFromInput = React.useCallback(
    (value: string) => {
      const scope = normalizeScope(value)
      if (scope === null) return

      if (value.trim() !== String(scope)) {
        skipNextDebounce.current = true
        setScopeInput(String(scope))
      }
      void load(scope)
    },
    [load]
  )

  React.useEffect(() => {
    const timer = window.setTimeout(() => void load(defaultScope), 0)
    return () => window.clearTimeout(timer)
  }, [load])

  React.useEffect(() => {
    if (!inputTouched) return

    if (skipNextDebounce.current) {
      skipNextDebounce.current = false
      return
    }

    if (debounceTimer.current !== null) {
      window.clearTimeout(debounceTimer.current)
    }
    if (normalizeScope(scopeInput) === null) return

    debounceTimer.current = window.setTimeout(() => {
      requestFromInput(scopeInput)
    }, debounceDelay)

    return () => {
      if (debounceTimer.current !== null) {
        window.clearTimeout(debounceTimer.current)
      }
    }
  }, [inputTouched, requestFromInput, scopeInput])

  React.useEffect(() => {
    return () => {
      requestVersion.current += 1
      if (debounceTimer.current !== null) {
        window.clearTimeout(debounceTimer.current)
      }
    }
  }, [])

  function requestImmediately(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key !== "Enter") return
    event.preventDefault()
    if (debounceTimer.current !== null) {
      window.clearTimeout(debounceTimer.current)
    }
    requestFromInput(scopeInput)
  }

  const inputInvalid = normalizeScope(scopeInput) === null
  const peak = Math.max(0, ...data.map((point) => point.total))
  const yMaximum = Math.max(5, peak)
  const chartMinimumWidth = Math.max(480, data.length * 48)
  const noData =
    total !== null && (total === 0 || data.every((point) => point.total === 0))

  return (
    <Card size="sm" className="min-w-0" aria-busy={loading}>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-1">
          <span>过去</span>
          <Input
            className="h-7 w-14 px-1 text-center"
            value={scopeInput}
            inputMode="numeric"
            aria-label="扫描统计天数"
            aria-invalid={inputInvalid}
            onChange={(event) => {
              setInputTouched(true)
              setScopeInput(event.target.value)
            }}
            onKeyDown={requestImmediately}
          />
          <span>日扫描统计</span>
        </CardTitle>
        <CardDescription>按本地自然日统计当前账户的扫描任务</CardDescription>
        <CardAction>
          <Badge variant="outline">总数：{total ?? "—"}</Badge>
        </CardAction>
      </CardHeader>
      <CardContent className="min-w-0">
        {loading ? (
          <div className="grid h-64 place-items-center">
            <Spinner className="size-6" aria-label="正在加载扫描统计" />
          </div>
        ) : error ? (
          <div className="flex h-64 items-center">
            <ErrorAlert message={error} />
          </div>
        ) : noData ? (
          <div className="grid h-64 place-items-center text-muted-foreground">
            暂无数据
          </div>
        ) : (
          <div className="w-full overflow-x-auto pb-2">
            <ChartContainer
              config={chartConfig}
              className="h-64 w-full"
              style={{ minWidth: `${chartMinimumWidth}px` }}
            >
              <BarChart accessibilityLayer data={data} barCategoryGap="77.5%">
                <CartesianGrid vertical={false} />
                <XAxis
                  dataKey="label"
                  tickLine={false}
                  axisLine={false}
                  tickMargin={8}
                  interval={0}
                />
                <YAxis
                  domain={[0, yMaximum]}
                  tickCount={6}
                  allowDecimals={false}
                  tickLine={false}
                  axisLine={false}
                  width={32}
                />
                <ChartTooltip
                  shared={false}
                  content={({ active }) => (
                    <StatisticsTooltip
                      active={active}
                      segment={hoveredSegment}
                    />
                  )}
                />
                <ChartLegend content={<ChartLegendContent />} />
                <Bar
                  dataKey="unknown"
                  stackId="result"
                  fill="var(--color-unknown)"
                  onMouseEnter={(entry) => {
                    const point = entry.payload as ChartPoint
                    setHoveredSegment({
                      date: point.label,
                      result: "unknown",
                      value: point.unknown,
                    })
                  }}
                  onMouseLeave={() => setHoveredSegment(null)}
                />
                <Bar
                  dataKey="error"
                  stackId="result"
                  fill="var(--color-error)"
                  onMouseEnter={(entry) => {
                    const point = entry.payload as ChartPoint
                    setHoveredSegment({
                      date: point.label,
                      result: "error",
                      value: point.error,
                    })
                  }}
                  onMouseLeave={() => setHoveredSegment(null)}
                />
                <Bar
                  dataKey="found"
                  stackId="result"
                  fill="var(--color-found)"
                  onMouseEnter={(entry) => {
                    const point = entry.payload as ChartPoint
                    setHoveredSegment({
                      date: point.label,
                      result: "found",
                      value: point.found,
                    })
                  }}
                  onMouseLeave={() => setHoveredSegment(null)}
                />
                <Bar
                  dataKey="clean"
                  stackId="result"
                  fill="var(--color-clean)"
                  radius={[4, 4, 0, 0]}
                  onMouseEnter={(entry) => {
                    const point = entry.payload as ChartPoint
                    setHoveredSegment({
                      date: point.label,
                      result: "clean",
                      value: point.clean,
                    })
                  }}
                  onMouseLeave={() => setHoveredSegment(null)}
                />
              </BarChart>
            </ChartContainer>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

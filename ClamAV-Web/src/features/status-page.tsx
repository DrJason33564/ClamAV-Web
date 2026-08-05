import * as React from "react"
import {
  ActivityIcon,
  Clock3Icon,
  LoaderCircleIcon,
  MoonIcon,
  RefreshCwIcon,
  ScanSearchIcon,
  ShieldCheckIcon,
} from "lucide-react"

import { ErrorAlert } from "@/components/error-alert"
import { PageLayout } from "@/components/page-layout"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { toast } from "@/components/ui/toast"
import { ScanStatisticsCard } from "@/features/scan-statistics-card"
import { api } from "@/lib/api"
import { errorMessage, formatTimestamp } from "@/lib/format"
import type {
  ClamAVPowerResponse,
  StatusResponse,
  StatusSource,
} from "@/lib/types"
import { cn } from "@/lib/utils"

function readPollInterval() {
  const value = Number(localStorage.getItem("statusPollInterval") || "5")
  return Number.isInteger(value) && value > 0 ? value : 5
}

function sourceObject(source: StatusResponse["source"]): StatusSource {
  return typeof source === "object" && source !== null ? source : {}
}

export function StatusPage({ isAdmin }: { isAdmin: boolean }) {
  const [status, setStatus] = React.useState<StatusResponse | null>(null)
  const [error, setError] = React.useState("")
  const [loading, setLoading] = React.useState(false)
  const [powerError, setPowerError] = React.useState("")
  const [powerLoading, setPowerLoading] = React.useState(false)
  const [pollInterval, setPollInterval] = React.useState(readPollInterval)
  const [debugging, setDebugging] = React.useState(
    () => localStorage.getItem("debugging") === "true"
  )

  const load = React.useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      setStatus(await api<StatusResponse>("/api/status"))
    } catch (nextError) {
      setError(errorMessage(nextError))
    } finally {
      setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    queueMicrotask(() => void load())
  }, [load])

  React.useEffect(() => {
    const timer = window.setInterval(() => void load(), pollInterval * 1000)
    return () => window.clearInterval(timer)
  }, [load, pollInterval])

  React.useEffect(() => {
    const sync = () => {
      setPollInterval(readPollInterval())
      setDebugging(localStorage.getItem("debugging") === "true")
    }
    window.addEventListener("clamav-settings-change", sync)
    return () => window.removeEventListener("clamav-settings-change", sync)
  }, [])

  const source = sourceObject(status?.source ?? {})
  const scan = source.scan ?? {}
  const sleeping = source.clamd?.status === "sleep"
  const ready = status?.ping === "ready"
  const active = Boolean(scan.active_job_id)
  const stateLabel = sleeping
    ? "已休眠"
    : !ready
      ? "服务异常"
      : active
        ? "正在扫描"
        : "准备就绪"
  const resultLabel =
    scan.last_job_status === "running"
      ? "扫描进行中"
      : scan.last_job_result === "clean"
        ? "未发现威胁"
        : scan.last_job_result === "found"
          ? "发现威胁"
          : "暂无结果"

  async function togglePower() {
    const action = sleeping ? "wake" : "sleep"
    setPowerLoading(true)
    setPowerError("")
    try {
      await api<ClamAVPowerResponse>(`/api/clamav/${action}`, {
        method: "POST",
      })
      toast.add({
        title: sleeping ? "ClamAV 已唤醒" : "ClamAV 已进入休眠",
        type: "success",
      })
      await load()
    } catch (nextError) {
      setPowerError(errorMessage(nextError))
    } finally {
      setPowerLoading(false)
    }
  }

  return (
    <PageLayout
      title="状态首页"
      description="查看 ClamAV 运行状态和最近一次扫描结果"
      icon={ActivityIcon}
      actions={
        <Button
          variant="outline"
          size="sm"
          disabled={loading}
          onClick={() => void load()}
        >
          <RefreshCwIcon data-icon="inline-start" />
          刷新
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        {error && <ErrorAlert message={error} />}
        <div className="grid gap-4 md:grid-cols-3">
          <Card size="sm">
            <CardHeader>
              <CardTitle>ClamAV 状态</CardTitle>
              <CardDescription>守护进程连通情况</CardDescription>
            </CardHeader>
            <CardContent className="flex items-center gap-3">
              <ShieldCheckIcon aria-hidden="true" />
              <Badge
                variant={
                  sleeping ? "secondary" : ready ? "default" : "destructive"
                }
              >
                {stateLabel}
              </Badge>
            </CardContent>
          </Card>
          <Card size="sm">
            <CardHeader>
              <CardTitle>上次扫描时间</CardTitle>
              <CardDescription>ClamAV上次执行扫描的时间</CardDescription>
            </CardHeader>
            <CardContent className="flex items-center gap-3">
              <Clock3Icon aria-hidden="true" />
              <span>{formatTimestamp(source.clamd?.last_checked_at)}</span>
            </CardContent>
          </Card>
          <Card size="sm">
            <CardHeader>
              <CardTitle>最近扫描结果</CardTitle>
              <CardDescription>最后完成任务的检测结论</CardDescription>
            </CardHeader>
            <CardContent className="flex items-center gap-3">
              <ScanSearchIcon aria-hidden="true" />
              <Badge
                variant={
                  scan.last_job_result === "found"
                    ? "destructive"
                    : scan.last_job_result === "clean"
                      ? "success"
                      : "secondary"
                }
              >
                {resultLabel}
              </Badge>
            </CardContent>
          </Card>
        </div>
        <div className="grid items-start gap-4 md:grid-cols-[12rem_minmax(0,1fr)]">
          <Card
            size="sm"
            className="relative isolate size-48 self-start"
            aria-busy={powerLoading}
          >
            <div
              aria-hidden="true"
              className={cn(
                "engine-control-mark",
                (sleeping || !ready) && "grayscale"
              )}
            />
            <div aria-hidden="true" className="engine-control-rings" />
            <CardHeader className="relative">
              <CardTitle>
                <Badge
                  variant={
                    sleeping ? "secondary" : ready ? "success" : "destructive"
                  }
                >
                  {sleeping ? "引擎已休眠" : ready ? "引擎运行中" : "引擎异常"}
                </Badge>
              </CardTitle>
              <CardAction>
                <Button
                  variant={sleeping ? "secondary" : "outline"}
                  size="icon-lg"
                  className="rounded-full"
                  aria-label={
                    powerLoading && sleeping
                      ? "正在唤醒 ClamAV"
                      : sleeping
                        ? "唤醒 ClamAV"
                        : "休眠 ClamAV"
                  }
                  aria-busy={powerLoading && sleeping}
                  title={
                    !isAdmin
                      ? "仅管理员可以休眠或唤醒 ClamAV"
                      : active
                        ? "扫描进行中，暂时无法休眠"
                        : sleeping
                          ? "唤醒 ClamAV"
                          : "休眠 ClamAV"
                  }
                  disabled={
                    !isAdmin ||
                    powerLoading ||
                    active ||
                    !status ||
                    (!sleeping && !ready)
                  }
                  onClick={() => void togglePower()}
                >
                  {powerLoading && sleeping ? (
                    <LoaderCircleIcon
                      className="animate-spin"
                      aria-hidden="true"
                    />
                  ) : (
                    <MoonIcon
                      className={cn(sleeping && "fill-current")}
                      aria-hidden="true"
                    />
                  )}
                </Button>
              </CardAction>
            </CardHeader>
          </Card>
          <ScanStatisticsCard />
        </div>
        {powerError && <ErrorAlert message={powerError} />}
        {!ready && !sleeping && status && (
          <ErrorAlert
            message={`${status.ping}: ${status.ping_message || source.message || "ClamAV 尚未准备好"}`}
          />
        )}
        {debugging && (
          <pre className="max-h-80 overflow-auto rounded-xl bg-muted p-4 text-xs">
            {JSON.stringify(status, null, 2)}
          </pre>
        )}
      </div>
    </PageLayout>
  )
}

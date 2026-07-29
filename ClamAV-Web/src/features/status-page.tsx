import * as React from "react"
import {
  ActivityIcon,
  Clock3Icon,
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
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { api } from "@/lib/api"
import { errorMessage, formatTimestamp } from "@/lib/format"
import type { StatusResponse, StatusSource } from "@/lib/types"

function readPollInterval() {
  const value = Number(localStorage.getItem("statusPollInterval") || "5")
  return Number.isInteger(value) && value > 0 ? value : 5
}

function sourceObject(source: StatusResponse["source"]): StatusSource {
  return typeof source === "object" && source !== null ? source : {}
}

export function StatusPage() {
  const [status, setStatus] = React.useState<StatusResponse | null>(null)
  const [error, setError] = React.useState("")
  const [loading, setLoading] = React.useState(false)
  const [pollInterval, setPollInterval] = React.useState(readPollInterval)
  const [debugging, setDebugging] = React.useState(
    () => localStorage.getItem("debugging") === "true",
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
    void load()
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
  const ready = status?.ping === "ready"
  const active = Boolean(scan.active_job_id)
  const stateLabel = !ready ? "服务异常" : active ? "正在扫描" : "准备就绪"
  const resultLabel =
    scan.last_job_status === "running"
      ? "扫描进行中"
      : scan.last_job_result === "clean"
        ? "未发现威胁"
        : scan.last_job_result === "found"
          ? "发现威胁"
          : "暂无结果"

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
              <Badge variant={ready ? "default" : "destructive"}>
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
        {!ready && status && (
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

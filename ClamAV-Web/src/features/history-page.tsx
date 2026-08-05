import * as React from "react"
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  HistoryIcon,
  RefreshCwIcon,
  Trash2Icon,
} from "lucide-react"

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
import { Input } from "@/components/ui/input"
import { Separator } from "@/components/ui/separator"
import { toast } from "@/components/ui/toast"
import { api, jsonRequest, sleep } from "@/lib/api"
import { errorMessage, formatCompactDate } from "@/lib/format"
import type { DetectionDetail, HistoryItem, HistoryLookup } from "@/lib/types"

const pageSize = 10

function resultLabel(result: HistoryItem["result"]) {
  if (result === "clean") return "完成"
  if (result === "found") return "发现威胁"
  if (result === "error") return "错误"
  return "未知 / 运行中"
}

export function HistoryPage() {
  const [items, setItems] = React.useState<HistoryItem[]>([])
  const [total, setTotal] = React.useState(0)
  const [page, setPage] = React.useState(1)
  const [pageInput, setPageInput] = React.useState("1")
  const [loading, setLoading] = React.useState(false)
  const [detail, setDetail] = React.useState<DetectionDetail | null>(null)
  const mounted = React.useRef(true)

  React.useEffect(() => {
    return () => {
      mounted.current = false
    }
  }, [])

  const load = React.useCallback(async (nextPage = 1) => {
    setLoading(true)
    setDetail(null)
    try {
      const start = (nextPage - 1) * pageSize + 1
      const begin = await api<{ lookup_id: string }>(
        `/api/results/lookups?scope=${start}-${nextPage * pageSize}`,
        { method: "POST" }
      )
      let lookup = await api<HistoryLookup>(
        `/api/results/lookups/${encodeURIComponent(begin.lookup_id)}`
      )
      while (lookup.status === "pending" && mounted.current) {
        await sleep(500)
        lookup = await api<HistoryLookup>(
          `/api/results/lookups/${encodeURIComponent(begin.lookup_id)}`
        )
      }
      if (lookup.status === "failed") {
        throw new Error(lookup.error || "历史任务加载失败")
      }
      if (mounted.current) {
        setItems(lookup.results ?? [])
        setTotal(lookup.total ?? 0)
        setPage(nextPage)
      }
    } catch (error) {
      toast.add({
        type: "error",
        title: "历史任务加载失败",
        description: errorMessage(error),
      })
    } finally {
      if (mounted.current) setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    queueMicrotask(() => void load(1))
  }, [load])

  React.useEffect(() => {
    queueMicrotask(() => setPageInput(String(page)))
  }, [page])

  async function openDetail(jobId: string) {
    try {
      setDetail(
        await api<DetectionDetail>(
          "/api/results/detection",
          jsonRequest("POST", { job_id: jobId })
        )
      )
    } catch (error) {
      toast.add({
        type: "error",
        title: "任务详情加载失败",
        description: errorMessage(error),
      })
    }
  }

  async function clean() {
    if (!confirm("清理所有历史任务及其文件？此操作不可恢复")) return
    try {
      const result = await api<{ deleted: number }>(
        "/api/results/clean?clean_all=Y",
        { method: "POST" }
      )
      toast.add({
        type: "success",
        title: "历史任务已清理",
        description: `删除了 ${result.deleted ?? 0} 个文件`,
      })
      await load(1)
    } catch (error) {
      toast.add({
        type: "error",
        title: "清理失败",
        description: errorMessage(error),
      })
    }
  }

  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  function jumpToPage(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key !== "Enter") return
    event.preventDefault()

    const requestedPage = Number(pageInput)
    const integerPage = Number.isFinite(requestedPage)
      ? Math.trunc(requestedPage)
      : 1
    const targetPage = Math.min(totalPages, Math.max(1, integerPage))

    setPageInput(String(targetPage))
    if (targetPage !== page) void load(targetPage)
  }

  return (
    <PageLayout
      title="历史任务"
      description="查询手动和定时扫描的历史结果与检测日志"
      icon={HistoryIcon}
      actions={
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={loading}
            onClick={() => void load(page)}
          >
            <RefreshCwIcon data-icon="inline-start" />
            刷新
          </Button>
          <Button variant="destructive" size="sm" onClick={() => void clean()}>
            <Trash2Icon data-icon="inline-start" />
            清理
          </Button>
        </div>
      }
    >
      {detail ? (
        <div className="flex flex-col gap-4">
          <Button
            variant="outline"
            size="sm"
            className="self-start"
            onClick={() => setDetail(null)}
          >
            <ChevronLeftIcon data-icon="inline-start" />
            返回列表
          </Button>
          <Card size="sm">
            <CardHeader>
              <CardTitle>{detail.job_id}</CardTitle>
              <CardDescription>扫描检测详情</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-4">
              {detail.detections.map((detection) => (
                <div
                  key={`${detection.source_file}-${detection.detection_reason}`}
                  className="flex flex-col gap-1 rounded-lg bg-muted p-3 text-sm"
                >
                  <strong>{detection.source_file}</strong>
                  <span className="text-muted-foreground">
                    {detection.detection_reason}
                  </span>
                </div>
              ))}
              <Separator />
              <div className="flex flex-col gap-2">
                <strong>检出日志</strong>
                <pre className="max-h-64 overflow-auto rounded-lg bg-muted p-3 text-xs">
                  {detail.original || "无检出日志"}
                </pre>
              </div>
              <div className="flex flex-col gap-2">
                <strong>任务日志</strong>
                <pre className="max-h-80 overflow-auto rounded-lg bg-muted p-3 text-xs">
                  {detail.log || "无任务日志"}
                </pre>
              </div>
            </CardContent>
          </Card>
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          {items.map((item) => (
            <Card key={item.id} size="sm">
              <CardHeader>
                <CardTitle>{item.id}</CardTitle>
                <CardDescription>
                  {formatCompactDate(item.date)} ·{" "}
                  {item.type === "manual" ? "手动扫描" : "定时任务"}
                </CardDescription>
                <CardAction>
                  <Badge
                    variant={
                      item.result === "found" ? "destructive" : "secondary"
                    }
                  >
                    {resultLabel(item.result)}
                  </Badge>
                </CardAction>
              </CardHeader>
              <CardContent>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => void openDetail(item.id)}
                >
                  查看详情
                  <ChevronRightIcon data-icon="inline-end" />
                </Button>
              </CardContent>
            </Card>
          ))}
          {!items.length && (
            <Card size="sm">
              <CardHeader>
                <CardTitle>{loading ? "正在加载" : "暂无历史任务"}</CardTitle>
                <CardDescription>尚未找到可显示的扫描结果</CardDescription>
              </CardHeader>
            </Card>
          )}
          <div className="flex items-center justify-center gap-3">
            <Button
              variant="outline"
              size="icon-sm"
              aria-label="上一页"
              disabled={page <= 1 || loading}
              onClick={() => void load(page - 1)}
            >
              <ChevronLeftIcon data-icon="inline-start" />
            </Button>
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <span>第</span>
              <Input
                type="text"
                inputMode="numeric"
                pattern="[0-9]*"
                aria-label="当前页码"
                className="w-16 text-center"
                value={pageInput}
                disabled={loading}
                onChange={(event) =>
                  setPageInput(event.target.value.replace(/\D/g, ""))
                }
                onFocus={(event) => event.currentTarget.select()}
                onKeyDown={jumpToPage}
              />
              <span>
                / {totalPages} 页，共 {total} 项
              </span>
            </div>
            <Button
              variant="outline"
              size="icon-sm"
              aria-label="下一页"
              disabled={page >= totalPages || loading}
              onClick={() => void load(page + 1)}
            >
              <ChevronRightIcon data-icon="inline-start" />
            </Button>
          </div>
        </div>
      )}
    </PageLayout>
  )
}

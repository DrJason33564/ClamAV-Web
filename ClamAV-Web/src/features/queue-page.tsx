import * as React from "react"
import {
  ListOrderedIcon,
  RefreshCwIcon,
  SaveIcon,
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
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { toast } from "@/components/ui/toast"
import { api, jsonRequest } from "@/lib/api"
import { actionLabel, errorMessage, formatTimestamp } from "@/lib/format"
import type { QueueItem } from "@/lib/types"

export function QueuePage() {
  const [items, setItems] = React.useState<QueueItem[]>([])
  const [loading, setLoading] = React.useState(false)
  const [positions, setPositions] = React.useState<Record<string, string>>({})

  const load = React.useCallback(async () => {
    setLoading(true)
    try {
      setItems(await api<QueueItem[]>("/api/scans"))
    } catch (error) {
      toast.add({
        type: "error",
        title: "任务队列加载失败",
        description: errorMessage(error),
      })
    } finally {
      setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    queueMicrotask(() => void load())
  }, [load])

  async function reorder(item: QueueItem) {
    const queueNumber = Number(positions[item.id])
    if (!Number.isInteger(queueNumber) || queueNumber < 1) return
    try {
      await api(
        "/api/scans/reorder",
        jsonRequest("POST", {
          id: item.id,
          queue_number: queueNumber,
        })
      )
      toast.add({ type: "success", title: "任务顺序已更新" })
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: "排序失败",
        description: errorMessage(error),
      })
    }
  }

  async function cancel(item: QueueItem) {
    if (!confirm(`删除排队任务 ${item.id}？`)) return
    try {
      await api(
        "/api/scans/cancel",
        jsonRequest("POST", { id: item.id, cancel: "Y" })
      )
      toast.add({ type: "success", title: "排队任务已删除" })
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: "删除失败",
        description: errorMessage(error),
      })
    }
  }

  return (
    <PageLayout
      title="任务队列"
      description="查看正在运行和等待中的手动扫描任务"
      icon={ListOrderedIcon}
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
      <div className="flex flex-col gap-3">
        {items.map((item) => {
          const running = item.status === "running"
          return (
            <Card key={item.id} size="sm">
              <CardHeader>
                <CardTitle className="truncate">{item.id}</CardTitle>
                <CardDescription>
                  {item.targets.join("、") || "暂无扫描目标"}
                </CardDescription>
                <CardAction>
                  <Badge variant={running ? "default" : "secondary"}>
                    {running ? "正在扫描" : `队列 ${item.queue_number}`}
                  </Badge>
                </CardAction>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <div className="grid gap-2 text-sm sm:grid-cols-2">
                  <span>处理方式：{actionLabel(item.action)}</span>
                  <span>
                    开始时间：
                    {running ? formatTimestamp(item.started_at) : "排队中"}
                  </span>
                </div>
                {!running && (
                  <div className="flex flex-wrap items-end gap-2">
                    <Field className="max-w-40">
                      <FieldLabel htmlFor={`queue-${item.id}`}>
                        目标序号
                      </FieldLabel>
                      <Input
                        id={`queue-${item.id}`}
                        type="text"
                        inputMode="numeric"
                        pattern="[0-9]*"
                        className="w-10 text-center"
                        value={positions[item.id] ?? ""}
                        onChange={(event) =>
                          setPositions((current) => ({
                            ...current,
                            [item.id]: event.target.value.replace(/\D/g, ""),
                          }))
                        }
                      />
                    </Field>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => void reorder(item)}
                    >
                      <SaveIcon data-icon="inline-start" />
                      调整顺序
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={() => void cancel(item)}
                    >
                      <Trash2Icon data-icon="inline-start" />
                      删除任务
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>
          )
        })}
        {!items.length && (
          <Card size="sm">
            <CardHeader>
              <CardTitle>{loading ? "正在加载" : "队列为空"}</CardTitle>
              <CardDescription>
                当前没有正在运行或等待中的手动扫描任务
              </CardDescription>
            </CardHeader>
          </Card>
        )}
      </div>
    </PageLayout>
  )
}

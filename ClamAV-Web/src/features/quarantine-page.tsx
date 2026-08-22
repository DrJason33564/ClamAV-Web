import * as React from "react"
import {
  ArchiveRestoreIcon,
  RefreshCwIcon,
  ShieldAlertIcon,
  Trash2Icon,
} from "lucide-react"

import { PageLayout } from "@/components/page-layout"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { toast } from "@/components/ui/toast"
import { api, sleep } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type { QuarantineLookup, QuarantineSubject } from "@/lib/types"

export function QuarantinePage() {
  const [items, setItems] = React.useState<QuarantineSubject[]>([])
  const [loading, setLoading] = React.useState(false)
  const mounted = React.useRef(true)

  React.useEffect(() => {
    return () => {
      mounted.current = false
    }
  }, [])

  const load = React.useCallback(async () => {
    setLoading(true)
    try {
      const begin = await api<{ lookup_id: string }>(
        "/api/quarantine/lookups",
        { method: "POST" }
      )
      let lookup = await api<QuarantineLookup>(
        `/api/quarantine/lookups/${encodeURIComponent(begin.lookup_id)}`
      )
      while (lookup.status === "pending" && mounted.current) {
        await sleep(500)
        lookup = await api<QuarantineLookup>(
          `/api/quarantine/lookups/${encodeURIComponent(begin.lookup_id)}`
        )
      }
      if (lookup.status === "failed") {
        throw new Error(lookup.error || "隔离区加载失败")
      }
      if (mounted.current) setItems(lookup.subjects ?? [])
    } catch (error) {
      toast.add({
        type: "error",
        title: "隔离区加载失败",
        description: errorMessage(error),
      })
    } finally {
      if (mounted.current) setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    queueMicrotask(() => void load())
  }, [load])

  async function action(
    item: QuarantineSubject,
    operation: "recover" | "delete"
  ) {
    const label = operation === "recover" ? "恢复" : "永久删除"
    if (!confirm(`${label}“${item.name}”？`)) return
    try {
      await api(
        `/api/quarantine/${operation}/${encodeURIComponent(item.name)}`,
        { method: "POST" }
      )
      toast.add({ type: "success", title: `文件${label}成功` })
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: `文件${label}失败`,
        description: errorMessage(error),
      })
    }
  }

  async function clean() {
    if (!confirm("永久删除隔离区中的所有文件？")) return
    try {
      await api("/api/quarantine/clean?clean_all=Y", { method: "POST" })
      toast.add({ type: "success", title: "隔离区已清空" })
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: "清理失败",
        description: errorMessage(error),
      })
    }
  }

  return (
    <PageLayout
      title="隔离区"
      description="查看、恢复或永久删除隔离文件"
      icon={ShieldAlertIcon}
      actions={
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={loading}
            onClick={() => void load()}
          >
            <RefreshCwIcon data-icon="inline-start" />
            刷新
          </Button>
          <Button variant="destructive" size="sm" onClick={() => void clean()}>
            <Trash2Icon data-icon="inline-start" />
            清空
          </Button>
        </div>
      }
    >
      <div className="flex flex-col gap-3">
        {items.map((item) => (
          <Card key={item.name} size="sm">
            <CardHeader>
              <CardTitle className="truncate">{item.name}</CardTitle>
              <CardDescription className="truncate">
                原始路径：{item.source_file}
              </CardDescription>
            </CardHeader>
            <CardContent className="flex flex-wrap gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => void action(item, "recover")}
              >
                <ArchiveRestoreIcon data-icon="inline-start" />
                恢复
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => void action(item, "delete")}
              >
                <Trash2Icon data-icon="inline-start" />
                永久删除
              </Button>
            </CardContent>
          </Card>
        ))}
        {!items.length && (
          <Card size="sm">
            <CardHeader>
              <CardTitle>{loading ? "正在加载" : "隔离区为空"}</CardTitle>
              <CardDescription>没有可显示的隔离文件</CardDescription>
            </CardHeader>
          </Card>
        )}
      </div>
    </PageLayout>
  )
}

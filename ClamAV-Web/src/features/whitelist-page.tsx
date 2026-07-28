import * as React from "react"
import { PlusIcon, ShieldCheckIcon, Trash2Icon } from "lucide-react"

import { FileBrowser } from "@/components/file-browser"
import { PageLayout } from "@/components/page-layout"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { toast } from "@/components/ui/toast"
import { api, jsonRequest } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type { WhitelistEntry, WhitelistResponse } from "@/lib/types"

export function WhitelistPage() {
  const [entries, setEntries] = React.useState<WhitelistEntry[]>([])
  const [selection, setSelection] = React.useState<string[]>([])

  const load = React.useCallback(async () => {
    try {
      const data = await api<WhitelistResponse>("/api/whitelist")
      setEntries(data.entries ?? [])
    } catch (error) {
      toast.add({
        type: "error",
        title: "信任区加载失败",
        description: errorMessage(error),
      })
    }
  }, [])

  React.useEffect(() => {
    void load()
  }, [load])

  async function add() {
    const path = selection[0]
    if (!path) return
    try {
      await api<WhitelistResponse>(
        "/api/whitelist",
        jsonRequest("POST", { path }),
      )
      setSelection([])
      toast.add({ type: "success", title: "已添加到信任区" })
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: "添加失败",
        description: errorMessage(error),
      })
    }
  }

  async function remove(path: string) {
    if (!confirm(`从信任区删除 ${path}？`)) return
    try {
      await api<WhitelistResponse>(
        "/api/whitelist",
        jsonRequest("DELETE", { path }),
      )
      toast.add({ type: "success", title: "信任区条目已删除" })
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
      title="信任区"
      description="管理不参与病毒扫描的可信文件和目录"
      icon={ShieldCheckIcon}
    >
      <div className="grid gap-5 xl:grid-cols-2">
        <div className="flex flex-col gap-4">
          <FileBrowser value={selection} onValueChange={setSelection} />
          <Button disabled={!selection.length} onClick={() => void add()}>
            <PlusIcon data-icon="inline-start" />
            添加到信任区
          </Button>
        </div>
        <div className="flex flex-col gap-3">
          {entries.map((entry) => (
            <Card key={entry.path} size="sm">
              <CardHeader>
                <CardTitle className="truncate">{entry.path}</CardTitle>
                <CardDescription>配置文件第 {entry.line} 行</CardDescription>
                <CardAction>
                  <Button
                    variant="destructive"
                    size="icon-sm"
                    aria-label={`删除 ${entry.path}`}
                    onClick={() => void remove(entry.path)}
                  >
                    <Trash2Icon data-icon="inline-start" />
                  </Button>
                </CardAction>
              </CardHeader>
            </Card>
          ))}
          {!entries.length && (
            <Card size="sm">
              <CardHeader>
                <CardTitle>暂无信任区条目</CardTitle>
                <CardDescription>
                  从左侧选择文件或目录后加入信任区
                </CardDescription>
              </CardHeader>
            </Card>
          )}
        </div>
      </div>
    </PageLayout>
  )
}

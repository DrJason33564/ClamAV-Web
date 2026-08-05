import * as React from "react"
import { PlayIcon, ScanSearchIcon } from "lucide-react"

import { ActionSelect } from "@/components/action-select"
import { FileBrowser } from "@/components/file-browser"
import { PageLayout } from "@/components/page-layout"
import { Button } from "@/components/ui/button"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { toast } from "@/components/ui/toast"
import { api, jsonRequest } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type { ScanAction } from "@/lib/types"

export function ManualScanPage() {
  const [targets, setTargets] = React.useState<string[]>([])
  const [action, setAction] = React.useState<ScanAction>("warn")
  const [submitting, setSubmitting] = React.useState(false)

  async function startScan() {
    if (!targets.length) return
    setSubmitting(true)
    try {
      const result = await api<{ id: string; status: string }>(
        "/api/scans",
        jsonRequest("POST", { targets, action })
      )
      setTargets([])
      toast.add({
        type: "success",
        title: "扫描任务已提交",
        description: `${result.id} · ${result.status}`,
      })
    } catch (error) {
      toast.add({
        type: "error",
        title: "扫描任务提交失败",
        description: errorMessage(error),
      })
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <PageLayout
      title="手动扫描"
      description="选择一个或多个文件、目录并启动即时扫描"
      icon={ScanSearchIcon}
    >
      <div className="flex flex-col gap-5">
        <FieldGroup>
          <Field>
            <FieldLabel>检出后的处理方式</FieldLabel>
            <ActionSelect value={action} onValueChange={setAction} />
            <FieldDescription>
              移动至隔离区和直接删除将直接操作文件
            </FieldDescription>
          </Field>
        </FieldGroup>
        <FileBrowser
          mode="multiple"
          value={targets}
          onValueChange={setTargets}
        />
        <div className="flex justify-end">
          <Button
            disabled={!targets.length || submitting}
            onClick={() => void startScan()}
          >
            <PlayIcon data-icon="inline-start" />
            {submitting ? "正在提交" : "开始扫描"}
          </Button>
        </div>
      </div>
    </PageLayout>
  )
}

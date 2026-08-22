import * as React from "react"
import {
  CalendarClockIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SaveIcon,
  Trash2Icon,
} from "lucide-react"

import { ActionSelect } from "@/components/action-select"
import { FileBrowser } from "@/components/file-browser"
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
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { toast } from "@/components/ui/toast"
import { api, jsonRequest } from "@/lib/api"
import { actionLabel, errorMessage } from "@/lib/format"
import type { CronRule, CronRulesResponse, ScanAction } from "@/lib/types"

type CronDraft = Omit<CronRule, "id">

const emptyDraft: CronDraft = {
  enabled: true,
  minute: "30",
  hour: "3",
  day: "*",
  month: "*",
  weekday: "*",
  target: "",
  action: "warn",
  wake: false,
}

export function CronPage() {
  const [rules, setRules] = React.useState<CronRule[]>([])
  const [draft, setDraft] = React.useState<CronDraft>(emptyDraft)
  const [editingId, setEditingId] = React.useState("")
  const [loading, setLoading] = React.useState(false)

  const load = React.useCallback(async () => {
    setLoading(true)
    try {
      const data = await api<CronRulesResponse>("/api/cron/rules")
      setRules(data.rules ?? [])
    } catch (error) {
      toast.add({
        type: "error",
        title: "定时任务加载失败",
        description: errorMessage(error),
      })
    } finally {
      setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    queueMicrotask(() => void load())
  }, [load])

  function update<K extends keyof CronDraft>(key: K, value: CronDraft[K]) {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  function reset() {
    setEditingId("")
    setDraft(emptyDraft)
  }

  function edit(rule: CronRule) {
    const { id, ...nextDraft } = rule
    setEditingId(id)
    setDraft(nextDraft)
  }

  async function save() {
    try {
      const url = editingId
        ? `/api/cron/rules/${encodeURIComponent(editingId)}`
        : "/api/cron/rules"
      await api<CronRulesResponse>(
        url,
        jsonRequest(editingId ? "PUT" : "POST", draft)
      )
      toast.add({
        type: "success",
        title: editingId ? "定时任务已更新" : "定时任务已创建",
      })
      reset()
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: "定时任务保存失败",
        description: errorMessage(error),
      })
    }
  }

  async function setEnabled(rule: CronRule, enabled: boolean) {
    try {
      await api<CronRulesResponse>(
        `/api/cron/rules/${encodeURIComponent(rule.id)}/enabled`,
        jsonRequest("PATCH", { enabled })
      )
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: "状态更新失败",
        description: errorMessage(error),
      })
    }
  }

  async function remove(rule: CronRule) {
    if (!confirm(`删除定时任务 ${rule.id}？`)) return
    try {
      await api<CronRulesResponse>(
        `/api/cron/rules/${encodeURIComponent(rule.id)}`,
        { method: "DELETE" }
      )
      if (editingId === rule.id) reset()
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: "删除失败",
        description: errorMessage(error),
      })
    }
  }

  async function reloadCron() {
    try {
      await api<CronRulesResponse>("/api/cron/reload", { method: "POST" })
      toast.add({ type: "success", title: "Cron 配置已重新加载" })
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: "重新加载失败",
        description: errorMessage(error),
      })
    }
  }

  const expression = [
    draft.minute,
    draft.hour,
    draft.day,
    draft.month,
    draft.weekday,
  ].join(" ")

  return (
    <PageLayout
      title="定时任务"
      description="创建、编辑和启停周期扫描规则"
      icon={CalendarClockIcon}
      actions={
        <Button variant="outline" size="sm" onClick={() => void reloadCron()}>
          <RefreshCwIcon data-icon="inline-start" />
          重新加载
        </Button>
      }
    >
      <div className="grid gap-5 xl:grid-cols-[minmax(0,1.2fr)_minmax(22rem,0.8fr)]">
        <div className="flex flex-col gap-5">
          <Card size="sm">
            <CardHeader>
              <CardTitle>{editingId ? "编辑规则" : "新建规则"}</CardTitle>
              <CardDescription>
                当前表达式：<code>{expression}</code>
              </CardDescription>
              <CardAction>
                {editingId && (
                  <Button variant="ghost" size="sm" onClick={reset}>
                    <PlusIcon data-icon="inline-start" />
                    新建
                  </Button>
                )}
              </CardAction>
            </CardHeader>
            <CardContent>
              <FieldGroup>
                <div className="grid gap-3 sm:grid-cols-5">
                  {(
                    [
                      ["minute", "分钟"],
                      ["hour", "小时"],
                      ["day", "日期"],
                      ["month", "月份"],
                      ["weekday", "星期"],
                    ] as const
                  ).map(([key, label]) => (
                    <Field key={key}>
                      <FieldLabel htmlFor={`cron-${key}`}>{label}</FieldLabel>
                      <Input
                        id={`cron-${key}`}
                        value={draft[key]}
                        onChange={(event) => update(key, event.target.value)}
                      />
                    </Field>
                  ))}
                </div>
                <Field>
                  <FieldLabel htmlFor="cron-target">扫描目标</FieldLabel>
                  <Input
                    id="cron-target"
                    value={draft.target}
                    placeholder="/scan/path"
                    onChange={(event) => update("target", event.target.value)}
                  />
                  <FieldDescription>
                    可以直接输入，也可以从下方文件浏览器选择
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel>检出后的处理方式</FieldLabel>
                  <ActionSelect
                    value={draft.action}
                    onValueChange={(value: ScanAction) =>
                      update("action", value)
                    }
                  />
                </Field>
                <Field orientation="horizontal">
                  <div className="flex flex-1 flex-col gap-1">
                    <FieldLabel htmlFor="cron-enabled">启用规则</FieldLabel>
                    <FieldDescription>
                      保存后立即参与 Cron 调度
                    </FieldDescription>
                  </div>
                  <Switch
                    id="cron-enabled"
                    checked={draft.enabled}
                    onCheckedChange={(enabled) => update("enabled", enabled)}
                  />
                </Field>
                <Field orientation="horizontal">
                  <div className="flex flex-1 flex-col gap-1">
                    <FieldLabel htmlFor="cron-wake">扫描前唤醒引擎</FieldLabel>
                    <FieldDescription>
                      执行规则时自动唤醒处于休眠状态的 ClamAV
                    </FieldDescription>
                  </div>
                  <Switch
                    id="cron-wake"
                    checked={draft.wake}
                    onCheckedChange={(wake) => update("wake", wake)}
                  />
                </Field>
              </FieldGroup>
            </CardContent>
          </Card>

          <FileBrowser
            value={draft.target ? [draft.target] : []}
            onValueChange={(paths) => update("target", paths[0] ?? "")}
          />

          <div className="flex justify-end">
            <Button disabled={!draft.target} onClick={() => void save()}>
              <SaveIcon data-icon="inline-start" />
              {editingId ? "更新规则" : "保存规则"}
            </Button>
          </div>
        </div>

        <div className="flex flex-col gap-3">
          {rules.map((rule) => (
            <Card key={rule.id} size="sm">
              <CardHeader>
                <CardTitle>
                  {[
                    rule.minute,
                    rule.hour,
                    rule.day,
                    rule.month,
                    rule.weekday,
                  ].join(" ")}
                </CardTitle>
                <CardDescription className="truncate">
                  {rule.target}
                </CardDescription>
                <CardAction>
                  <Badge variant={rule.enabled ? "default" : "secondary"}>
                    {rule.enabled ? "启用" : "禁用"}
                  </Badge>
                </CardAction>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <span className="text-sm text-muted-foreground">
                  {actionLabel(rule.action)}
                </span>
                {rule.wake && (
                  <Badge variant="outline" className="self-start">
                    扫描前唤醒引擎
                  </Badge>
                )}
                <div className="flex flex-wrap items-center gap-2">
                  <Switch
                    size="sm"
                    checked={rule.enabled}
                    aria-label={`${rule.enabled ? "禁用" : "启用"}规则`}
                    onCheckedChange={(enabled) =>
                      void setEnabled(rule, enabled)
                    }
                  />
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => edit(rule)}
                  >
                    <PencilIcon data-icon="inline-start" />
                    编辑
                  </Button>
                  <Button
                    variant="destructive"
                    size="sm"
                    onClick={() => void remove(rule)}
                  >
                    <Trash2Icon data-icon="inline-start" />
                    删除
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
          {!rules.length && (
            <Card size="sm">
              <CardHeader>
                <CardTitle>{loading ? "正在加载" : "暂无定时规则"}</CardTitle>
                <CardDescription>
                  使用左侧表单创建第一条定时扫描任务
                </CardDescription>
              </CardHeader>
            </Card>
          )}
        </div>
      </div>
    </PageLayout>
  )
}

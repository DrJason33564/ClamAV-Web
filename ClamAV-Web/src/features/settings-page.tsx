import * as React from "react"
import { SettingsIcon } from "lucide-react"

import { PageLayout } from "@/components/page-layout"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { toast } from "@/components/ui/toast"

function notifyChange() {
  window.dispatchEvent(new Event("clamav-settings-change"))
}

export function SettingsPage() {
  const [debugging, setDebugging] = React.useState(
    () => localStorage.getItem("debugging") === "true",
  )
  const [interval, setIntervalValue] = React.useState(
    () => localStorage.getItem("statusPollInterval") || "5",
  )
  const intervalValid = /^[1-9]\d*$/.test(interval)

  function updateDebugging(nextValue: boolean) {
    setDebugging(nextValue)
    localStorage.setItem("debugging", String(nextValue))
    notifyChange()
    toast.add({
      type: "success",
      title: "设置已保存",
      description: nextValue ? "已开启调试数据" : "已关闭调试数据",
    })
  }

  function updateInterval(nextValue: string) {
    const digits = nextValue.replace(/\D/g, "")
    setIntervalValue(digits)
    if (/^[1-9]\d*$/.test(digits)) {
      localStorage.setItem("statusPollInterval", digits)
      notifyChange()
    }
  }

  return (
    <PageLayout
      title="设置面板"
      description="配置当前浏览器中的界面偏好和状态刷新频率"
      icon={SettingsIcon}
    >
      <FieldGroup>
        <Field data-invalid={!intervalValid}>
          <FieldLabel htmlFor="poll-interval">状态轮询间隔（秒）</FieldLabel>
          <Input
            id="poll-interval"
            inputMode="numeric"
            value={interval}
            aria-invalid={!intervalValid}
            onChange={(event) => updateInterval(event.target.value)}
          />
          <FieldDescription>
            使用大于 0 的整数；修改后状态首页立即采用新间隔
          </FieldDescription>
        </Field>
        <Field orientation="horizontal">
          <div className="flex flex-1 flex-col gap-1">
            <FieldLabel htmlFor="debug-mode">调试模式</FieldLabel>
            <FieldDescription>
              在状态首页显示后端返回的原始状态数据
            </FieldDescription>
          </div>
          <Switch
            id="debug-mode"
            checked={debugging}
            onCheckedChange={updateDebugging}
          />
        </Field>
      </FieldGroup>
    </PageLayout>
  )
}

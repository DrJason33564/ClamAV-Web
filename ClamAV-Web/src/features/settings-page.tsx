import * as React from "react"
import { SettingsIcon, Trash2Icon } from "lucide-react"

import { PageLayout } from "@/components/page-layout"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
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
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "@/components/ui/toast"
import { api, jsonRequest } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type { CurrentUser, ServiceConfig } from "@/lib/types"

function notifyChange() {
  window.dispatchEvent(new Event("clamav-settings-change"))
}

export function SettingsPage({
  user,
  isAdmin,
  isTimedock,
  onSignedOut,
}: {
  user: CurrentUser
  isAdmin: boolean
  isTimedock: boolean
  onSignedOut: () => void
}) {
  const [debugging, setDebugging] = React.useState(
    () => localStorage.getItem("debugging") === "true"
  )
  const [interval, setIntervalValue] = React.useState(
    () => localStorage.getItem("statusPollInterval") || "5"
  )
  const [currentPassword, setCurrentPassword] = React.useState("")
  const [newPassword, setNewPassword] = React.useState("")
  const [deletePassword, setDeletePassword] = React.useState("")
  const [deleteOpen, setDeleteOpen] = React.useState(false)
  const [serviceInterval, setServiceInterval] = React.useState("")
  const [saving, setSaving] = React.useState(false)
  const intervalValid = /^[1-9]\d*$/.test(interval)
  const serviceValid =
    /^\d+$/.test(serviceInterval) &&
    Number(serviceInterval) >= 5 &&
    Number(serviceInterval) <= 86400

  React.useEffect(() => {
    if (!isAdmin) return
    api<ServiceConfig>("/api/config")
      .then((config) =>
        setServiceInterval(String(config.history_index_refresh_interval))
      )
      .catch((error) =>
        toast.add({
          type: "error",
          title: "服务配置加载失败",
          description: errorMessage(error),
        })
      )
  }, [isAdmin])

  function updateDebugging(nextValue: boolean) {
    setDebugging(nextValue)
    localStorage.setItem("debugging", String(nextValue))
    notifyChange()
  }

  function updateInterval(nextValue: string) {
    const digits = nextValue.replace(/\D/g, "")
    setIntervalValue(digits)
    if (/^[1-9]\d*$/.test(digits)) {
      localStorage.setItem("statusPollInterval", digits)
      notifyChange()
    }
  }

  async function changePassword(event: React.FormEvent) {
    event.preventDefault()
    setSaving(true)
    try {
      await api(
        "/api/auth/password",
        jsonRequest("PUT", {
          current_password: currentPassword,
          new_password: newPassword,
        })
      )
      toast.add({ type: "success", title: "密码已修改，请重新登录" })
      onSignedOut()
    } catch (error) {
      toast.add({
        type: "error",
        title: "密码修改失败",
        description: errorMessage(error),
      })
    } finally {
      setSaving(false)
    }
  }

  async function deleteAccount() {
    setSaving(true)
    try {
      await api(
        "/api/auth/account",
        jsonRequest("DELETE", { password: deletePassword })
      )
      onSignedOut()
    } catch (error) {
      toast.add({
        type: "error",
        title: "账户注销失败",
        description: errorMessage(error),
      })
    } finally {
      setSaving(false)
    }
  }

  async function saveService(event: React.FormEvent) {
    event.preventDefault()
    if (!serviceValid) return
    setSaving(true)
    try {
      await api(
        "/api/config",
        jsonRequest("PATCH", {
          history_index_refresh_interval: Number(serviceInterval),
        })
      )
      toast.add({ type: "success", title: "服务配置已保存" })
    } catch (error) {
      toast.add({
        type: "error",
        title: "服务配置保存失败",
        description: errorMessage(error),
      })
    } finally {
      setSaving(false)
    }
  }

  return (
    <PageLayout
      title="设置"
      description="管理界面偏好、个人账户与服务配置"
      icon={SettingsIcon}
    >
      <Tabs defaultValue="interface">
        <TabsList>
          <TabsTrigger value="interface">界面</TabsTrigger>
          <TabsTrigger value="account">账户</TabsTrigger>
          {isAdmin && <TabsTrigger value="service">服务</TabsTrigger>}
        </TabsList>
        <TabsContent value="interface" className="mt-4">
          <Card>
            <CardHeader>
              <CardTitle>当前浏览器</CardTitle>
              <CardDescription>这些设置只保存在本机浏览器中</CardDescription>
            </CardHeader>
            <CardContent>
              <FieldGroup>
                <Field data-invalid={!intervalValid}>
                  <FieldLabel htmlFor="poll-interval">
                    状态轮询间隔（秒）
                  </FieldLabel>
                  <Input
                    id="poll-interval"
                    inputMode="numeric"
                    value={interval}
                    aria-invalid={!intervalValid}
                    onChange={(event) => updateInterval(event.target.value)}
                  />
                  <FieldDescription>
                    正整数，默认每 5 秒刷新一次
                  </FieldDescription>
                </Field>
                <Field orientation="horizontal">
                  <div className="flex flex-1 flex-col gap-1">
                    <FieldLabel htmlFor="debugging">显示调试数据</FieldLabel>
                    <FieldDescription>
                      在部分页面中显示原始响应
                    </FieldDescription>
                  </div>
                  <Switch
                    id="debugging"
                    checked={debugging}
                    onCheckedChange={updateDebugging}
                  />
                </Field>
              </FieldGroup>
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="account" className="mt-4 flex flex-col gap-4">
          <Card>
            <CardHeader>
              <CardTitle>{user.username}</CardTitle>
              <CardDescription>当前登录账户</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-wrap gap-2">
              <Badge>{isAdmin ? "管理员" : "普通用户"}</Badge>
              {isTimedock && (
                <Badge variant="outline">
                  拾光坞账户：{user.timedock_account || "未绑定"}
                </Badge>
              )}
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>修改密码</CardTitle>
              <CardDescription>
                修改后所有设备上的登录会话都会失效
              </CardDescription>
            </CardHeader>
            <CardContent>
              <form onSubmit={changePassword}>
                <FieldGroup>
                  <Field>
                    <FieldLabel htmlFor="current-password">当前密码</FieldLabel>
                    <Input
                      id="current-password"
                      type="password"
                      autoComplete="current-password"
                      required
                      value={currentPassword}
                      onChange={(event) =>
                        setCurrentPassword(event.target.value)
                      }
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="new-password">新密码</FieldLabel>
                    <Input
                      id="new-password"
                      type="password"
                      autoComplete="new-password"
                      required
                      value={newPassword}
                      onChange={(event) => setNewPassword(event.target.value)}
                    />
                  </Field>
                  <Button
                    className="self-start"
                    type="submit"
                    disabled={saving || !currentPassword || !newPassword}
                  >
                    {saving && <Spinner data-icon="inline-start" />}修改密码
                  </Button>
                </FieldGroup>
              </form>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>注销账户</CardTitle>
              <CardDescription>
                永久删除账户及其任务、规则、历史和隔离数据
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Button variant="destructive" onClick={() => setDeleteOpen(true)}>
                <Trash2Icon data-icon="inline-start" />
                注销账户
              </Button>
            </CardContent>
          </Card>
        </TabsContent>
        {isAdmin && (
          <TabsContent value="service" className="mt-4">
            <Card>
              <CardHeader>
                <CardTitle>历史索引</CardTitle>
                <CardDescription>配置后台历史索引器的刷新频率</CardDescription>
              </CardHeader>
              <CardContent>
                <form onSubmit={saveService}>
                  <FieldGroup>
                    <Field data-invalid={!serviceValid}>
                      <FieldLabel htmlFor="service-interval">
                        索引刷新间隔（秒）
                      </FieldLabel>
                      <Input
                        id="service-interval"
                        inputMode="numeric"
                        value={serviceInterval}
                        aria-invalid={!serviceValid}
                        onChange={(event) =>
                          setServiceInterval(
                            event.target.value.replace(/\D/g, "")
                          )
                        }
                      />
                      <FieldDescription>允许 5–86400 秒</FieldDescription>
                    </Field>
                    <Button
                      className="self-start"
                      type="submit"
                      disabled={saving || !serviceValid}
                    >
                      {saving && <Spinner data-icon="inline-start" />}
                      保存服务配置
                    </Button>
                  </FieldGroup>
                </form>
              </CardContent>
            </Card>
          </TabsContent>
        )}
      </Tabs>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>永久注销账户？</AlertDialogTitle>
            <AlertDialogDescription>
              此操作会清理该账户的所有数据，且无法撤销，存在运行中扫描时后端会拒绝注销
            </AlertDialogDescription>
          </AlertDialogHeader>
          <Field>
            <FieldLabel htmlFor="delete-password">输入当前密码确认</FieldLabel>
            <Input
              id="delete-password"
              type="password"
              autoComplete="current-password"
              value={deletePassword}
              onChange={(event) => setDeletePassword(event.target.value)}
            />
          </Field>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={saving || !deletePassword}
              onClick={() => void deleteAccount()}
            >
              {saving && <Spinner data-icon="inline-start" />}永久注销
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </PageLayout>
  )
}

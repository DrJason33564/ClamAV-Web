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

function integerInRange(value: string, minimum: number, maximum: number) {
  return (
    /^[1-9]\d*$/.test(value) &&
    Number(value) >= minimum &&
    Number(value) <= maximum
  )
}

function validClamAVSleepTimer(value: string) {
  return value === "0" || integerInRange(value, 600, 9223372036)
}

function validIPAddress(address: string) {
  if (!address.includes(":")) {
    const parts = address.split(".")
    return (
      parts.length === 4 &&
      parts.every(
        (part) =>
          /^(0|[1-9]\d{0,2})$/.test(part) && Number(part) >= 0 && Number(part) <= 255
      )
    )
  }

  if (!/^[0-9a-f:.]+$/i.test(address)) return false
  try {
    new URL(`http://[${address}]/`)
    return true
  } catch {
    return false
  }
}

function trustedProxyAddresses(value: string) {
  return value.split(",").map((address) => address.trim())
}

function validTrustedProxy(value: string) {
  if (!value.trim()) return true
  const addresses = trustedProxyAddresses(value)
  return addresses.every(
    (address) => address !== "" && validIPAddress(address)
  )
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
  const [clamavSleepTimer, setClamAVSleepTimer] = React.useState("")
  const [loginMaxTries, setLoginMaxTries] = React.useState("")
  const [loginMaxTriesOverall, setLoginMaxTriesOverall] = React.useState("")
  const [loginCooldownInterval, setLoginCooldownInterval] =
    React.useState("")
  const [trustedReverseProxy, setTrustedReverseProxy] = React.useState("")
  const [saving, setSaving] = React.useState(false)
  const intervalValid = /^[1-9]\d*$/.test(interval)
  const serviceIntervalValid = integerInRange(serviceInterval, 5, 86400)
  const clamavSleepTimerValid = validClamAVSleepTimer(clamavSleepTimer)
  const loginMaxTriesValid = integerInRange(loginMaxTries, 1, 10000)
  const loginMaxTriesOverallValid =
    integerInRange(loginMaxTriesOverall, 1, 1000000) &&
    loginMaxTriesValid &&
    Number(loginMaxTriesOverall) >= Number(loginMaxTries)
  const loginCooldownIntervalValid = integerInRange(
    loginCooldownInterval,
    1,
    86400
  )
  const trustedReverseProxyValid = validTrustedProxy(trustedReverseProxy)
  const serviceValid =
    serviceIntervalValid &&
    clamavSleepTimerValid &&
    loginMaxTriesValid &&
    loginMaxTriesOverallValid &&
    loginCooldownIntervalValid &&
    trustedReverseProxyValid

  const applyServiceConfig = React.useCallback((config: ServiceConfig) => {
    setServiceInterval(String(config.history_index_refresh_interval))
    setClamAVSleepTimer(String(config.clamav_sleep_timer))
    setLoginMaxTries(String(config.web_login_max_tries))
    setLoginMaxTriesOverall(String(config.web_login_max_tries_overall))
    setLoginCooldownInterval(String(config.web_login_cooldown_interval))
    setTrustedReverseProxy(config.server_trusted_reverseproxy)
  }, [])

  React.useEffect(() => {
    if (!isAdmin) return
    api<ServiceConfig>("/api/config")
      .then(applyServiceConfig)
      .catch((error) =>
        toast.add({
          type: "error",
          title: "服务配置加载失败",
          description: errorMessage(error),
        })
      )
  }, [applyServiceConfig, isAdmin])

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
      const config = await api<ServiceConfig>(
        "/api/config",
        jsonRequest("PATCH", {
          history_index_refresh_interval: Number(serviceInterval),
          clamav_sleep_timer: Number(clamavSleepTimer),
          web_login_max_tries: Number(loginMaxTries),
          web_login_max_tries_overall: Number(loginMaxTriesOverall),
          web_login_cooldown_interval: Number(loginCooldownInterval),
          server_trusted_reverseproxy: trustedReverseProxy.trim()
            ? trustedProxyAddresses(trustedReverseProxy).join(",")
            : "",
        })
      )
      applyServiceConfig(config)
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
                <CardTitle>服务配置</CardTitle>
                <CardDescription>调整服务端配置项</CardDescription>
              </CardHeader>
              <CardContent>
                <form onSubmit={saveService}>
                  <FieldGroup>
                    <Field data-invalid={!serviceIntervalValid}>
                      <FieldLabel htmlFor="service-interval">
                        索引刷新间隔（秒）
                      </FieldLabel>
                      <Input
                        id="service-interval"
                        inputMode="numeric"
                        value={serviceInterval}
                        aria-invalid={!serviceIntervalValid}
                        onChange={(event) =>
                          setServiceInterval(
                            event.target.value.replace(/\D/g, "")
                          )
                        }
                      />
                      <FieldDescription>允许 5–86400 秒</FieldDescription>
                    </Field>
                    <Field data-invalid={!clamavSleepTimerValid}>
                      <FieldLabel htmlFor="clamav-sleep-timer">
                        ClamAV 定时休眠（秒）
                      </FieldLabel>
                      <Input
                        id="clamav-sleep-timer"
                        inputMode="numeric"
                        value={clamavSleepTimer}
                        aria-invalid={!clamavSleepTimerValid}
                        onChange={(event) =>
                          setClamAVSleepTimer(
                            event.target.value.replace(/\D/g, "")
                          )
                        }
                      />
                      <FieldDescription>
                        输入 0 关闭定时休眠，启用时至少 600 秒，默认 3600 秒
                      </FieldDescription>
                    </Field>
                    <Field data-invalid={!loginMaxTriesValid}>
                      <FieldLabel htmlFor="login-max-tries">
                        单 IP 登录请求上限
                      </FieldLabel>
                      <Input
                        id="login-max-tries"
                        inputMode="numeric"
                        value={loginMaxTries}
                        aria-invalid={!loginMaxTriesValid}
                        onChange={(event) =>
                          setLoginMaxTries(
                            event.target.value.replace(/\D/g, "")
                          )
                        }
                      />
                      <FieldDescription>
                        十分钟内允许 1–10000 次登录请求
                      </FieldDescription>
                    </Field>
                    <Field data-invalid={!loginMaxTriesOverallValid}>
                      <FieldLabel htmlFor="login-max-tries-overall">
                        全局登录请求上限
                      </FieldLabel>
                      <Input
                        id="login-max-tries-overall"
                        inputMode="numeric"
                        value={loginMaxTriesOverall}
                        aria-invalid={!loginMaxTriesOverallValid}
                        onChange={(event) =>
                          setLoginMaxTriesOverall(
                            event.target.value.replace(/\D/g, "")
                          )
                        }
                      />
                      <FieldDescription>
                        十分钟内允许全局 1-1000000 次登录请求，不小于单 IP 上限
                      </FieldDescription>
                    </Field>
                    <Field data-invalid={!loginCooldownIntervalValid}>
                      <FieldLabel htmlFor="login-cooldown-interval">
                        登录冷却时间（秒）
                      </FieldLabel>
                      <Input
                        id="login-cooldown-interval"
                        inputMode="numeric"
                        value={loginCooldownInterval}
                        aria-invalid={!loginCooldownIntervalValid}
                        onChange={(event) =>
                          setLoginCooldownInterval(
                            event.target.value.replace(/\D/g, "")
                          )
                        }
                      />
                      <FieldDescription>允许 1–86400 秒</FieldDescription>
                    </Field>
                    <Field data-invalid={!trustedReverseProxyValid}>
                      <FieldLabel htmlFor="trusted-reverse-proxy">
                        可信反向代理
                      </FieldLabel>
                      <Input
                        id="trusted-reverse-proxy"
                        autoComplete="off"
                        placeholder="留空表示不信任任何X-Forwarded-For标头"
                        value={trustedReverseProxy}
                        aria-invalid={!trustedReverseProxyValid}
                        onChange={(event) =>
                          setTrustedReverseProxy(event.target.value)
                        }
                      />
                      <FieldDescription>
                        填写单个或多个 IPv4 或 IPv6 地址、用英文半角逗号分隔，不含端口和 CIDR
                      </FieldDescription>
                    </Field>
                    <Field>
                      <FieldDescription>
                        修改登录限制会清空当前登录计数和冷却状态，保存后立即生效
                      </FieldDescription>
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

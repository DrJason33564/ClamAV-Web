import * as React from "react"
import { CheckIcon, UserRoundPlusIcon } from "lucide-react"

import { AuthLayout } from "@/components/auth-layout"
import { ErrorAlert } from "@/components/error-alert"
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
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { api, ApiError, jsonRequest } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type { AuthResponse, CurrentUser, FirstRunResponse } from "@/lib/types"

type Mode = "register" | "existing"

export function FirstRunPage() {
  const [mode, setMode] = React.useState<Mode>("register")
  const [username, setUsername] = React.useState("")
  const [password, setPassword] = React.useState("")
  const [registerToken, setRegisterToken] = React.useState("")
  const [error, setError] = React.useState("")
  const [loading, setLoading] = React.useState(false)

  React.useEffect(() => {
    api<FirstRunResponse>("/api/first-run/status", undefined, {
      ignoreUnauthorized: true,
    })
      .then((state) => {
        if (state.first_run === "completed") window.location.replace("/login/")
      })
      .catch((nextError) => setError(errorMessage(nextError)))
  }, [])

  async function complete() {
    await api("/api/first-run/complete", { method: "POST" })
    window.location.replace("/#status")
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    setLoading(true)
    setError("")
    try {
      if (mode === "register") {
        await api(
          "/api/auth/register",
          jsonRequest("POST", {
            username,
            password,
            token: registerToken,
          }),
          { ignoreUnauthorized: true }
        )
      }
      const session = await api<AuthResponse>(
        "/api/auth/login",
        jsonRequest("POST", { username, password }),
        { ignoreUnauthorized: true }
      )
      if (session.role !== "admin")
        throw new Error("只有管理员可以完成首次运行设置")
      await complete()
    } catch (nextError) {
      if (
        mode === "register" &&
        nextError instanceof ApiError &&
        nextError.status === 403
      ) {
        setError(
          "管理员注册令牌无效，请检查服务端 ADMIN_REGISTER_TOKEN 配置"
        )
      } else if (
        mode === "register" &&
        nextError instanceof ApiError &&
        nextError.status === 409
      ) {
        setError("首次管理员注册不可用，请检查首次运行状态")
      } else {
        setError(errorMessage(nextError))
      }
    } finally {
      setLoading(false)
    }
  }

  React.useEffect(() => {
    api<CurrentUser>("/api/auth/me", undefined, { ignoreUnauthorized: true })
      .then((user) => {
        if (user.role === "admin") void complete()
      })
      .catch(() => undefined)
  }, [])

  return (
    <AuthLayout>
      <Card>
        <CardHeader>
          <CardTitle>完成首次设置</CardTitle>
          <CardDescription>
            创建首位管理员，或使用已创建的管理员完成初始化
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Tabs
            value={mode}
            onValueChange={(value) => {
              setMode(value as Mode)
              setError("")
            }}
          >
            <TabsList className="grid w-full grid-cols-2">
              <TabsTrigger value="register">注册管理员</TabsTrigger>
              <TabsTrigger value="existing">已有管理员</TabsTrigger>
            </TabsList>
            <form className="mt-5" onSubmit={submit}>
              <FieldGroup>
                {error && <ErrorAlert message={error} />}
                <Field>
                  <FieldLabel htmlFor="setup-username">用户名</FieldLabel>
                  <Input
                    id="setup-username"
                    autoComplete="username"
                    autoFocus
                    required
                    value={username}
                    onChange={(event) => setUsername(event.target.value)}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="setup-password">密码</FieldLabel>
                  <Input
                    id="setup-password"
                    type="password"
                    autoComplete={
                      mode === "register" ? "new-password" : "current-password"
                    }
                    required
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                  />
                </Field>
                {mode === "register" && (
                  <Field>
                    <FieldLabel htmlFor="setup-register-token">
                      管理员注册令牌
                    </FieldLabel>
                    <Input
                      id="setup-register-token"
                      type="password"
                      autoComplete="off"
                      required
                      value={registerToken}
                      onChange={(event) => setRegisterToken(event.target.value)}
                    />
                    <FieldDescription>
                      输入服务端 ADMIN_REGISTER_TOKEN 的值
                    </FieldDescription>
                  </Field>
                )}
                <Button
                  type="submit"
                  disabled={
                    loading ||
                    !username ||
                    !password ||
                    (mode === "register" && !registerToken)
                  }
                >
                  {loading ? (
                    <Spinner data-icon="inline-start" />
                  ) : mode === "register" ? (
                    <UserRoundPlusIcon data-icon="inline-start" />
                  ) : (
                    <CheckIcon data-icon="inline-start" />
                  )}
                  {loading ? "正在完成设置" : "完成设置"}
                </Button>
              </FieldGroup>
            </form>
          </Tabs>
        </CardContent>
      </Card>
    </AuthLayout>
  )
}

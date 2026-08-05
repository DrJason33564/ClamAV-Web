import * as React from "react"
import { LogInIcon } from "lucide-react"

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
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { api, jsonRequest } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type { AuthResponse, CurrentUser, FirstRunResponse } from "@/lib/types"

export function LoginPage() {
  const [username, setUsername] = React.useState("")
  const [password, setPassword] = React.useState("")
  const [error, setError] = React.useState("")
  const [loading, setLoading] = React.useState(false)

  React.useEffect(() => {
    async function prepare() {
      try {
        const state = await api<FirstRunResponse>(
          "/api/first-run/status",
          undefined,
          { ignoreUnauthorized: true }
        )
        if (state.first_run !== "completed") {
          window.location.replace("/first_run/")
          return
        }
        await api<CurrentUser>("/api/auth/me", undefined, {
          ignoreUnauthorized: true,
        })
        window.location.replace("/#status")
      } catch {
        // 未登录时保留登录表单
      }
    }
    void prepare()
  }, [])

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    setLoading(true)
    setError("")
    try {
      await api<AuthResponse>(
        "/api/auth/login",
        jsonRequest("POST", { username, password }),
        { ignoreUnauthorized: true }
      )
      window.location.replace("/#status")
    } catch (nextError) {
      setError(errorMessage(nextError))
    } finally {
      setLoading(false)
    }
  }

  return (
    <AuthLayout prominentBrand>
      <Card>
        <CardHeader className="items-center text-center">
          <CardTitle className="text-2xl">登录</CardTitle>
          <CardDescription className="text-base">
            使用 ClamAV Web 账户继续
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit}>
            <FieldGroup>
              {error && <ErrorAlert message={error} />}
              <Field>
                <FieldLabel htmlFor="login-username">用户名</FieldLabel>
                <Input
                  id="login-username"
                  autoComplete="username"
                  autoFocus
                  required
                  value={username}
                  onChange={(event) => setUsername(event.target.value)}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="login-password">密码</FieldLabel>
                <Input
                  id="login-password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                />
              </Field>
              <Button
                type="submit"
                disabled={loading || !username || !password}
              >
                {loading ? (
                  <Spinner data-icon="inline-start" />
                ) : (
                  <LogInIcon data-icon="inline-start" />
                )}
                {loading ? "正在登录" : "登录"}
              </Button>
            </FieldGroup>
          </form>
        </CardContent>
      </Card>
    </AuthLayout>
  )
}

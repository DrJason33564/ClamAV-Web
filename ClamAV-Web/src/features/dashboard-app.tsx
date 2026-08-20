import * as React from "react"
import {
  ActivityIcon,
  CalendarClockIcon,
  ChevronDownIcon,
  HistoryIcon,
  ListOrderedIcon,
  LogOutIcon,
  ScanSearchIcon,
  SettingsIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  UserCogIcon,
  UsersIcon,
  type LucideIcon,
} from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Separator } from "@/components/ui/separator"
import { Spinner } from "@/components/ui/spinner"
import { Toaster } from "@/components/ui/toast"
import { ApiError, api } from "@/lib/api"
import type { CurrentUser, FirstRunResponse, StatusResponse } from "@/lib/types"
import { cn } from "@/lib/utils"

const CronPage = React.lazy(async () => ({
  default: (await import("@/features/cron-page")).CronPage,
}))
const HistoryPage = React.lazy(async () => ({
  default: (await import("@/features/history-page")).HistoryPage,
}))
const ManualScanPage = React.lazy(async () => ({
  default: (await import("@/features/manual-scan-page")).ManualScanPage,
}))
const QuarantinePage = React.lazy(async () => ({
  default: (await import("@/features/quarantine-page")).QuarantinePage,
}))
const QueuePage = React.lazy(async () => ({
  default: (await import("@/features/queue-page")).QueuePage,
}))
const SettingsPage = React.lazy(async () => ({
  default: (await import("@/features/settings-page")).SettingsPage,
}))
const StatusPage = React.lazy(async () => ({
  default: (await import("@/features/status-page")).StatusPage,
}))
const UsersPage = React.lazy(async () => ({
  default: (await import("@/features/users-page")).UsersPage,
}))
const WhitelistPage = React.lazy(async () => ({
  default: (await import("@/features/whitelist-page")).WhitelistPage,
}))

type PageKey =
  | "status"
  | "manual"
  | "cron"
  | "queue"
  | "whitelist"
  | "history"
  | "quarantine"
  | "users"
  | "settings"

const pages: Array<{
  key: PageKey
  label: string
  icon: LucideIcon
  admin?: boolean
}> = [
  { key: "status", label: "状态首页", icon: ActivityIcon },
  { key: "manual", label: "手动扫描", icon: ScanSearchIcon },
  { key: "cron", label: "定时任务", icon: CalendarClockIcon },
  { key: "queue", label: "任务队列", icon: ListOrderedIcon },
  { key: "whitelist", label: "信任区", icon: ShieldCheckIcon },
  { key: "history", label: "历史任务", icon: HistoryIcon },
  { key: "quarantine", label: "隔离区", icon: ShieldAlertIcon },
  { key: "users", label: "用户管理", icon: UsersIcon, admin: true },
  { key: "settings", label: "设置", icon: SettingsIcon },
]

function navigate(page: PageKey) {
  window.location.hash = page
}

function readPage(isAdmin: boolean): PageKey {
  const value = window.location.hash.slice(1) as PageKey
  const entry = pages.find(({ key }) => key === value)
  return entry && (!entry.admin || isAdmin) ? entry.key : "status"
}

function Dashboard() {
  const [user, setUser] = React.useState<CurrentUser | null>(null)
  const [isTimedock, setIsTimedock] = React.useState(false)
  const [page, setPage] = React.useState<PageKey>("status")
  const [loading, setLoading] = React.useState(true)
  const [accountMenuOpen, setAccountMenuOpen] = React.useState(false)

  const loadSession = React.useCallback(async () => {
    const [nextUser, status] = await Promise.all([
      api<CurrentUser>("/api/auth/me", undefined, { ignoreUnauthorized: true }),
      api<StatusResponse>("/api/status", undefined, {
        ignoreUnauthorized: true,
      }),
    ])
    setUser(nextUser)
    setIsTimedock(status.is_timedock)
    setPage(readPage(nextUser.role === "admin"))
  }, [])

  React.useEffect(() => {
    async function boot() {
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
        await loadSession()
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) {
          window.location.replace("/login/")
          return
        }
        window.location.replace("/login/")
      } finally {
        setLoading(false)
      }
    }
    void boot()
  }, [loadSession])

  React.useEffect(() => {
    const syncPage = () => user && setPage(readPage(user.role === "admin"))
    const expire = () => window.location.replace("/login/")
    window.addEventListener("hashchange", syncPage)
    window.addEventListener("clamav-auth-expired", expire)
    return () => {
      window.removeEventListener("hashchange", syncPage)
      window.removeEventListener("clamav-auth-expired", expire)
    }
  }, [user])

  async function logout() {
    try {
      await api(
        "/api/auth/logout",
        { method: "POST" },
        { ignoreUnauthorized: true }
      )
    } finally {
      window.location.replace("/login/")
    }
  }

  if (loading || !user) {
    return (
      <main className="grid min-h-svh place-items-center bg-muted/30">
        <Spinner className="size-6" />
      </main>
    )
  }

  const isAdmin = user.role === "admin"
  let content: React.ReactNode
  if (page === "manual") content = <ManualScanPage />
  else if (page === "cron") content = <CronPage />
  else if (page === "queue") content = <QueuePage />
  else if (page === "whitelist") content = <WhitelistPage />
  else if (page === "history") content = <HistoryPage />
  else if (page === "quarantine") content = <QuarantinePage />
  else if (page === "users" && isAdmin)
    content = (
      <UsersPage
        currentUser={user}
        isTimedock={isTimedock}
        onSessionChange={loadSession}
      />
    )
  else if (page === "settings")
    content = (
      <SettingsPage
        user={user}
        isAdmin={isAdmin}
        isTimedock={isTimedock}
        onSignedOut={() => window.location.replace("/login/")}
      />
    )
  else content = <StatusPage isAdmin={isAdmin} />

  const visiblePages = pages.filter(
    (entry) => entry.key !== "users" && (!entry.admin || isAdmin)
  )

  return (
    <div className="min-h-svh bg-muted/30">
      <header className="border-b bg-background">
        <div className="mx-auto flex max-w-screen-2xl items-center gap-1 px-4 py-2 lg:px-6">
          <img
            src="/clamav-web-mark.png"
            alt=""
            className="size-12 object-contain"
          />
          <strong
            className="flex items-baseline gap-1 text-2xl tracking-tight"
            aria-label="ClamAV Web"
          >
            <span className="font-heading">ClamAV</span>
            <span className="font-brand-web font-medium text-muted-foreground">
              Web
            </span>
          </strong>
          <DropdownMenu
            open={accountMenuOpen}
            onOpenChange={setAccountMenuOpen}
          >
            <DropdownMenuTrigger
              render={<Button variant="ghost" className="ml-auto" />}
            >
              <UserCogIcon data-icon="inline-start" />
              {user.username}
              <ChevronDownIcon
                data-icon="inline-end"
                className={cn(
                  "transition-transform duration-200",
                  accountMenuOpen && "-rotate-180"
                )}
              />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuGroup>
                <DropdownMenuLabel>
                  {isAdmin ? "管理员" : "普通用户"}
                </DropdownMenuLabel>
                <DropdownMenuItem onClick={() => navigate("settings")}>
                  <SettingsIcon />
                  账户与设置
                </DropdownMenuItem>
                {isAdmin && (
                  <DropdownMenuItem onClick={() => navigate("users")}>
                    <UsersIcon />
                    用户管理
                  </DropdownMenuItem>
                )}
              </DropdownMenuGroup>
              <DropdownMenuSeparator />
              <DropdownMenuGroup>
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() => void logout()}
                >
                  <LogOutIcon />
                  退出登录
                </DropdownMenuItem>
              </DropdownMenuGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>
      <div className="mx-auto grid max-w-screen-2xl gap-4 p-4 lg:grid-cols-[13rem_minmax(0,1fr)] lg:p-6">
        <aside className="self-start overflow-x-auto rounded-xl border bg-background p-2 lg:overflow-visible">
          <nav className="flex min-w-max gap-1 lg:min-w-0 lg:flex-col">
            {visiblePages.map(({ key, label, icon: Icon }, index) => (
              <React.Fragment key={key}>
                {index === visiblePages.length - 1 && (
                  <Separator
                    orientation="vertical"
                    className="mx-1 lg:hidden"
                  />
                )}
                {index === visiblePages.length - 1 && (
                  <Separator className="my-1 hidden lg:block" />
                )}
                <Button
                  variant={page === key ? "secondary" : "ghost"}
                  className="justify-start"
                  onClick={() => navigate(key)}
                >
                  <Icon data-icon="inline-start" />
                  {label}
                </Button>
              </React.Fragment>
            ))}
          </nav>
        </aside>
        <main className="min-w-0">
          {isTimedock && !user.timedock_account && (
            <Alert className="mb-4">
              <ShieldAlertIcon />
              <AlertTitle>尚未绑定拾光坞账户</AlertTitle>
              <AlertDescription>
                绑定前无法浏览或提交扫描路径，请联系管理员
              </AlertDescription>
            </Alert>
          )}
          <React.Suspense
            fallback={
              <div className="grid min-h-48 place-items-center">
                <Spinner className="size-6" />
              </div>
            }
          >
            {content}
          </React.Suspense>
        </main>
      </div>
    </div>
  )
}

export function DashboardApp() {
  return (
    <Toaster>
      <Dashboard />
    </Toaster>
  )
}

import * as React from "react"
import {
  ActivityIcon,
  CalendarClockIcon,
  HistoryIcon,
  ListOrderedIcon,
  ScanSearchIcon,
  SettingsIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  type LucideIcon,
} from "lucide-react"

import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { Toaster } from "@/components/ui/toast"
import { CronPage } from "@/features/cron-page"
import { HistoryPage } from "@/features/history-page"
import { ManualScanPage } from "@/features/manual-scan-page"
import { QuarantinePage } from "@/features/quarantine-page"
import { QueuePage } from "@/features/queue-page"
import { SettingsPage } from "@/features/settings-page"
import { StatusPage } from "@/features/status-page"
import { WhitelistPage } from "@/features/whitelist-page"

type PageKey =
  | "status"
  | "manual"
  | "cron"
  | "queue"
  | "whitelist"
  | "history"
  | "quarantine"
  | "settings"

const pages: Array<{ key: PageKey; label: string; icon: LucideIcon }> = [
  { key: "status", label: "状态首页", icon: ActivityIcon },
  { key: "manual", label: "手动扫描", icon: ScanSearchIcon },
  { key: "cron", label: "定时任务", icon: CalendarClockIcon },
  { key: "queue", label: "任务队列", icon: ListOrderedIcon },
  { key: "whitelist", label: "信任区", icon: ShieldCheckIcon },
  { key: "history", label: "历史任务", icon: HistoryIcon },
  { key: "quarantine", label: "隔离区", icon: ShieldAlertIcon },
  { key: "settings", label: "设置", icon: SettingsIcon },
]

function PageContent({ page }: { page: PageKey }) {
  if (page === "manual") return <ManualScanPage />
  if (page === "cron") return <CronPage />
  if (page === "queue") return <QueuePage />
  if (page === "whitelist") return <WhitelistPage />
  if (page === "history") return <HistoryPage />
  if (page === "quarantine") return <QuarantinePage />
  if (page === "settings") return <SettingsPage />
  return <StatusPage />
}

export function App() {
  const [page, setPage] = React.useState<PageKey>("status")

  return (
    <Toaster>
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
              <span className="font-heading text-foreground">ClamAV</span>
              <span className="font-brand-web font-medium text-muted-foreground">
                Web
              </span>
            </strong>
          </div>
        </header>
        <div className="mx-auto grid max-w-screen-2xl gap-4 p-4 lg:grid-cols-[13rem_minmax(0,1fr)] lg:p-6">
          <aside className="self-start overflow-x-auto rounded-xl border bg-background p-2 lg:overflow-visible">
            <nav className="flex min-w-max gap-1 lg:min-w-0 lg:flex-col">
              {pages.map(({ key, label, icon: Icon }, index) => (
                <React.Fragment key={key}>
                  {index === pages.length - 1 && (
                    <Separator
                      orientation="vertical"
                      className="mx-1 lg:hidden"
                    />
                  )}
                  {index === pages.length - 1 && (
                    <Separator className="my-1 hidden lg:block" />
                  )}
                  <Button
                    variant={page === key ? "secondary" : "ghost"}
                    className="justify-start"
                    onClick={() => setPage(key)}
                  >
                    <Icon data-icon="inline-start" />
                    {label}
                  </Button>
                </React.Fragment>
              ))}
            </nav>
          </aside>
          <main className="min-w-0">
            <PageContent page={page} />
          </main>
        </div>
      </div>
    </Toaster>
  )
}

export default App

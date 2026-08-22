import * as React from "react"
import { InfoIcon } from "lucide-react"

import { ErrorAlert } from "@/components/error-alert"
import { PageLayout } from "@/components/page-layout"
import { Card, CardContent, CardHeader } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"
import { api } from "@/lib/api"
import { APP_VERSION } from "@/lib/app-meta"
import { errorMessage } from "@/lib/format"
import type { StatusResponse } from "@/lib/types"

const projectLinks = [
  {
    label: "GitHub 项目主页",
    href: "https://github.com/DrJason33564/ClamAV-Web",
    badge: "https://img.shields.io/badge/github-repo-blue?logo=github",
  },
  {
    label: "GitHub 最新 Release",
    href: "https://github.com/DrJason33564/ClamAV-Web/releases",
    badge:
      "https://img.shields.io/github/v/release/DrJason33564/ClamAV-Web?style=flat-square&logo=github",
  },
  {
    label: "项目许可证",
    href: "https://github.com/DrJason33564/ClamAV-Web/blob/main/LICENSE",
    badge:
      "https://img.shields.io/github/license/DrJason33564/ClamAV-Web?style=flat-square",
  },
]

function statusValue(value: string | undefined, loading: boolean) {
  if (loading && value === undefined) return "正在获取…"
  return value || "未知"
}

export function AboutPage() {
  const [status, setStatus] = React.useState<StatusResponse | null>(null)
  const [error, setError] = React.useState("")
  const [loading, setLoading] = React.useState(false)

  const load = React.useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      setStatus(await api<StatusResponse>("/api/status"))
    } catch (nextError) {
      setError(errorMessage(nextError))
    } finally {
      setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    queueMicrotask(() => void load())
  }, [load])

  const versionRows = [
    { label: "ClamAV-Web 版本", value: APP_VERSION },
    {
      label: "引擎版本",
      value: statusValue(status?.clamd_version, loading),
    },
    {
      label: "病毒库版本",
      value: statusValue(status?.database_version, loading),
    },
    {
      label: "病毒库发布日期",
      value: statusValue(status?.database_date, loading),
    },
  ]

  return (
    <PageLayout title="关于ClamAV-Web" icon={InfoIcon}>
      <div className="flex flex-col gap-4">
        {error && <ErrorAlert message={error} />}
        <Card size="sm" className="ring-0" aria-busy={loading}>
          <CardHeader>
            <div className="flex items-center justify-center gap-3">
              <img
                src="/clamav-web-mark.png"
                alt="ClamAV-Web 标志"
                className="size-16 shrink-0 object-contain"
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
            </div>
          </CardHeader>
          <CardContent className="flex flex-col gap-6">
            <dl className="mx-auto w-full max-w-2xl">
              {versionRows.map((row) => (
                <React.Fragment key={row.label}>
                  <div className="grid items-center gap-1 px-2 py-3 text-center sm:grid-cols-2 sm:gap-4">
                    <dt className="font-medium">{row.label}</dt>
                    <dd className="min-w-0 break-words">{row.value}</dd>
                  </div>
                  <Separator />
                </React.Fragment>
              ))}
            </dl>
            <nav
              className="flex flex-wrap items-center justify-center gap-2"
              aria-label="项目链接"
            >
              {projectLinks.map((link) => (
                <a
                  key={link.href}
                  href={link.href}
                  target="_blank"
                  rel="noreferrer"
                  aria-label={`${link.label}（在新标签页打开）`}
                >
                  <img
                    src={link.badge}
                    alt={link.label}
                    loading="lazy"
                    referrerPolicy="no-referrer"
                  />
                </a>
              ))}
            </nav>
          </CardContent>
        </Card>
      </div>
    </PageLayout>
  )
}

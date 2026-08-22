import type { LucideIcon } from "lucide-react"

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { cn } from "@/lib/utils"

type PageLayoutProps = {
  title: string
  description?: string
  icon: LucideIcon
  children: React.ReactNode
  actions?: React.ReactNode
}

export function PageLayout({
  title,
  description,
  icon: Icon,
  children,
  actions,
}: PageLayoutProps) {
  return (
    <Card>
      <CardHeader>
        <div
          className={cn(
            "flex gap-3",
            description ? "items-start" : "items-center"
          )}
        >
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted">
            <Icon aria-hidden="true" />
          </div>
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <CardTitle>{title}</CardTitle>
            {description && <CardDescription>{description}</CardDescription>}
          </div>
          {actions}
        </div>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  )
}

import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

export function AuthLayout({
  children,
  prominentBrand = false,
}: {
  children: ReactNode
  prominentBrand?: boolean
}) {
  return (
    <main className="grid min-h-svh place-items-center bg-muted/30 p-4">
      <div className="flex w-full max-w-sm flex-col gap-6">
        <div
          className={cn(
            "flex items-center justify-center gap-2",
            prominentBrand && "flex-col"
          )}
        >
          <img
            src="/clamav-web-mark.png"
            alt=""
            className={cn(
              "object-contain",
              prominentBrand ? "size-28" : "size-14"
            )}
          />
          <strong
            className={cn(
              "flex items-baseline gap-1 tracking-tight",
              prominentBrand ? "text-4xl" : "text-2xl"
            )}
            aria-label="ClamAV Web"
          >
            <span className="font-heading">ClamAV</span>
            <span className="font-brand-web font-medium text-muted-foreground">
              Web
            </span>
          </strong>
        </div>
        {children}
      </div>
    </main>
  )
}

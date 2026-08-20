import { StrictMode, type ReactNode } from "react"
import { createRoot } from "react-dom/client"

import "@/index.css"
import { ThemeProvider } from "@/components/theme-provider"

export function renderRoot(content: ReactNode) {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ThemeProvider>{content}</ThemeProvider>
    </StrictMode>
  )
}

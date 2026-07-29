import type { ScanAction } from "@/lib/types"

export function formatTimestamp(value?: string | null) {
  if (!value) return "未知"
  const normalized = value.replace(/([+-]\d{2})(\d{2})$/, "$1:$2")
  const date = new Date(normalized)
  if (Number.isNaN(date.getTime())) return value.replace("T", " ")
  return new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "medium",
    hour12: false,
  }).format(date)
}

export function formatCompactDate(value: string) {
  if (!/^\d{14}$/.test(value)) return formatTimestamp(value)
  return `${value.slice(0, 4)}-${value.slice(4, 6)}-${value.slice(6, 8)} ${value.slice(8, 10)}:${value.slice(10, 12)}:${value.slice(12, 14)}`
}

export function actionLabel(action: ScanAction) {
  if (action === "move") return "移动到隔离区"
  if (action === "remove") return "直接删除"
  return "仅告警"
}

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "未知错误"
}

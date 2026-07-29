import * as React from "react"
import {
  ChevronUpIcon,
  FileIcon,
  FolderIcon,
  FolderOpenIcon,
  RefreshCwIcon,
} from "lucide-react"

import { ErrorAlert } from "@/components/error-alert"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { api } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type { BrowseResponse } from "@/lib/types"

type FileBrowserProps = {
  mode?: "single" | "multiple"
  value: string[]
  onValueChange: (value: string[]) => void
}

export function FileBrowser({
  mode = "single",
  value,
  onValueChange,
}: FileBrowserProps) {
  const [data, setData] = React.useState<BrowseResponse | null>(null)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState("")

  const load = React.useCallback(async (path = "") => {
    setLoading(true)
    setError("")
    try {
      setData(
        await api<BrowseResponse>(
          `/api/browse?path=${encodeURIComponent(path)}`,
        ),
      )
    } catch (nextError) {
      setError(errorMessage(nextError))
    } finally {
      setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    void load()
  }, [load])

  function toggle(path: string, checked: boolean) {
    if (mode === "single") {
      onValueChange(checked ? [path] : [])
      return
    }
    onValueChange(
      checked
        ? Array.from(new Set([...value, path]))
        : value.filter((item) => item !== path),
    )
  }

  const activeRoot =
    data?.roots.find(
      (root) => data.path === root || data.path.startsWith(`${root}/`),
    ) ?? data?.roots[0]

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          items={(data?.roots ?? []).map((root) => ({
            label: root,
            value: root,
          }))}
          value={activeRoot}
          onValueChange={(root) => void load(root ?? "")}
        >
          <SelectTrigger className="min-w-48">
            <SelectValue placeholder="扫描根目录" />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              {(data?.roots ?? []).map((root) => (
                <SelectItem key={root} value={root}>
                  {root}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <Button
          variant="outline"
          size="sm"
          disabled={!data?.parent || loading}
          onClick={() => void load(data?.parent)}
        >
          <ChevronUpIcon data-icon="inline-start" />
          上一级
        </Button>
        <Button
          variant="ghost"
          size="sm"
          disabled={loading}
          onClick={() => void load(data?.path)}
        >
          <RefreshCwIcon data-icon="inline-start" />
          刷新
        </Button>
        <span className="min-w-0 flex-1 truncate text-sm text-muted-foreground">
          {data?.path || "正在读取目录…"}
        </span>
      </div>

      {error && <ErrorAlert message={error} />}

      <div className="flex min-h-64 flex-col overflow-hidden rounded-xl border">
        {data?.entries.length ? (
          data.entries.map((entry) => {
            const checked = value.includes(entry.path)
            return (
              <div
                key={entry.path}
                className="flex min-w-0 items-center gap-3 border-b px-3 py-2 last:border-b-0"
              >
                <Checkbox
                  aria-label={`选择 ${entry.name}`}
                  checked={checked}
                  onCheckedChange={(nextChecked) =>
                    toggle(entry.path, nextChecked === true)
                  }
                />
                {entry.is_dir ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="min-w-0 justify-start"
                    onClick={() => void load(entry.path)}
                  >
                    <FolderIcon data-icon="inline-start" />
                    <span className="truncate">{entry.name}</span>
                  </Button>
                ) : (
                  <div
                    className={buttonVariants({
                      variant: "ghost",
                      size: "sm",
                      className: "min-w-0 justify-start",
                    })}
                  >
                    <FileIcon data-icon="inline-start" aria-hidden="true" />
                    <span className="truncate">{entry.name}</span>
                  </div>
                )}
                <span className="ml-auto hidden truncate text-xs text-muted-foreground md:block">
                  {entry.path}
                </span>
              </div>
            )
          })
        ) : (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <FolderOpenIcon />
              </EmptyMedia>
              <EmptyTitle>{loading ? "正在加载" : "目录为空"}</EmptyTitle>
              <EmptyDescription>
                {loading ? "正在读取目录内容" : "此目录中没有可显示的项目"}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
      </div>

      <div className="flex min-h-7 flex-wrap gap-2">
        {value.map((path) => (
          <Badge key={path} variant="secondary">
            {path}
          </Badge>
        ))}
      </div>
    </div>
  )
}

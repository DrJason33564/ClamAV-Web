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
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { api } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type { BrowseResponse } from "@/lib/types"

const filenameScrollDelay = 2000
const filenameEdgeDelay = 1200
const filenameScrollSpeed = 24
const addressDebounceDelay = 3000

function normalizeBrowserPath(value: string, roots: string[]) {
  if (!value.startsWith("/") || value.includes("\0")) return null

  const segments: string[] = []
  for (const segment of value.split("/")) {
    if (!segment || segment === ".") continue
    if (segment === "..") {
      if (!segments.length) return null
      segments.pop()
      continue
    }
    segments.push(segment)
  }

  const normalized = `/${segments.join("/")}`
  const withinRoot = roots.some(
    (root) =>
      normalized === root ||
      (root === "/"
        ? normalized.startsWith("/")
        : normalized.startsWith(`${root}/`))
  )

  return withinRoot ? normalized : null
}

type MarqueeFilenameProps = {
  name: string
  visible: boolean
}

function MarqueeFilename({ name, visible }: MarqueeFilenameProps) {
  const elementRef = React.useRef<HTMLSpanElement>(null)

  React.useEffect(() => {
    const currentElement = elementRef.current
    if (!currentElement) return
    const element: HTMLSpanElement = currentElement

    // Drive scrollLeft directly so the marquee covers the exact overflow distance.
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)")
    let animationFrame = 0
    let delayTimer = 0
    let direction: 1 | -1 = 1
    let lastFrameTime = 0
    let moving = false
    let hovered = false
    let resumeOnMouseLeave = false

    function hasOverflow() {
      return element.scrollWidth - element.clientWidth > 1
    }

    function stop() {
      window.cancelAnimationFrame(animationFrame)
      window.clearTimeout(delayTimer)
      animationFrame = 0
      delayTimer = 0
      lastFrameTime = 0
      moving = false
    }

    function canAnimate() {
      return visible && !hovered && !reducedMotion.matches && hasOverflow()
    }

    function schedule(delay = filenameScrollDelay) {
      stop()
      if (!canAnimate()) {
        resumeOnMouseLeave = hovered && visible && hasOverflow()
        return
      }
      resumeOnMouseLeave = false
      delayTimer = window.setTimeout(() => {
        moving = true
        animationFrame = window.requestAnimationFrame(step)
      }, delay)
    }

    function step(timestamp: number) {
      if (hovered) {
        resumeOnMouseLeave = true
        stop()
        return
      }
      if (!canAnimate()) {
        stop()
        return
      }

      if (!lastFrameTime) lastFrameTime = timestamp
      const elapsed = Math.min(timestamp - lastFrameTime, 64)
      lastFrameTime = timestamp

      const maximum = element.scrollWidth - element.clientWidth
      const next =
        element.scrollLeft + direction * filenameScrollSpeed * (elapsed / 1000)

      if (direction === 1 && next >= maximum) {
        element.scrollLeft = maximum
        direction = -1
        schedule(filenameEdgeDelay)
        return
      }
      if (direction === -1 && next <= 0) {
        element.scrollLeft = 0
        direction = 1
        schedule(filenameScrollDelay)
        return
      }

      element.scrollLeft = next
      animationFrame = window.requestAnimationFrame(step)
    }

    function pauseOnMouseEnter() {
      hovered = true
      if (moving) {
        resumeOnMouseLeave = true
        stop()
      }
    }

    function resumeOnMouseExit() {
      hovered = false
      if (!resumeOnMouseLeave) return
      resumeOnMouseLeave = false
      schedule(0)
    }

    function handleReducedMotionChange() {
      if (reducedMotion.matches) stop()
      else schedule(filenameScrollDelay)
    }

    const resizeObserver = new ResizeObserver(() => {
      if (!hasOverflow()) {
        stop()
        element.scrollLeft = 0
      } else {
        schedule(filenameScrollDelay)
      }
    })

    element.addEventListener("mouseenter", pauseOnMouseEnter)
    element.addEventListener("mouseleave", resumeOnMouseExit)
    reducedMotion.addEventListener("change", handleReducedMotionChange)
    resizeObserver.observe(element)

    if (visible) {
      element.scrollLeft = 0
      schedule(filenameScrollDelay)
    } else {
      element.scrollLeft = 0
    }

    return () => {
      stop()
      resizeObserver.disconnect()
      reducedMotion.removeEventListener("change", handleReducedMotionChange)
      element.removeEventListener("mouseenter", pauseOnMouseEnter)
      element.removeEventListener("mouseleave", resumeOnMouseExit)
    }
  }, [name, visible])

  return (
    <span
      ref={elementRef}
      data-slot="file-browser-entry-name"
      title={name}
      className="block max-w-[798px] min-w-0 overflow-hidden whitespace-nowrap"
    >
      {name}
    </span>
  )
}

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
  const [addressInput, setAddressInput] = React.useState("")
  const [addressTouched, setAddressTouched] = React.useState(false)
  const [addressRequestError, setAddressRequestError] = React.useState(false)
  const [visibleEntries, setVisibleEntries] = React.useState<
    ReadonlySet<string>
  >(() => new Set())
  const requestIdRef = React.useRef(0)
  const addressDebounceTimerRef = React.useRef<number | null>(null)
  const selectionRef = React.useRef({ mode, value, onValueChange })
  const listRef = React.useRef<HTMLDivElement>(null)
  const addressInputId = React.useId()

  React.useEffect(() => {
    selectionRef.current = { mode, value, onValueChange }
  }, [mode, onValueChange, value])

  const clearAddressDebounce = React.useCallback(() => {
    if (addressDebounceTimerRef.current === null) return
    window.clearTimeout(addressDebounceTimerRef.current)
    addressDebounceTimerRef.current = null
  }, [])

  const load = React.useCallback(
    async (path = "", source: "navigation" | "address" = "navigation") => {
      // Only the newest navigation response may replace the directory currently shown.
      const requestId = ++requestIdRef.current
      setLoading(true)
      setError("")
      try {
        const nextData = await api<BrowseResponse>(
          `/api/browse?path=${encodeURIComponent(path)}`
        )
        if (requestId !== requestIdRef.current) return

        setData(nextData)
        setAddressInput(nextData.path)
        setAddressTouched(false)
        setAddressRequestError(false)

        if (source === "address" && !nextData.target.is_dir) {
          const selection = selectionRef.current
          selection.onValueChange(
            selection.mode === "single"
              ? [nextData.target.path]
              : Array.from(new Set([...selection.value, nextData.target.path]))
          )
        }
      } catch (nextError) {
        if (requestId === requestIdRef.current) {
          setError(errorMessage(nextError))
          if (source === "address") setAddressRequestError(true)
        }
      } finally {
        if (requestId === requestIdRef.current) setLoading(false)
      }
    },
    []
  )

  React.useEffect(() => {
    let active = true
    queueMicrotask(() => {
      if (active) void load()
    })
    return () => {
      active = false
      clearAddressDebounce()
      requestIdRef.current += 1
    }
  }, [clearAddressDebounce, load])

  React.useEffect(() => {
    const list = listRef.current
    if (!list) return

    const entries = list.querySelectorAll<HTMLElement>(
      '[data-slot="file-browser-entry"]'
    )
    const entryPaths = new Set(
      Array.from(entries, (entry) => entry.dataset.entryPath).filter(
        (path): path is string => Boolean(path)
      )
    )
    const observer = new IntersectionObserver(
      (observedEntries) => {
        setVisibleEntries((current) => {
          const next = new Set(
            Array.from(current).filter((path) => entryPaths.has(path))
          )
          let changed = next.size !== current.size

          for (const observedEntry of observedEntries) {
            const path = (observedEntry.target as HTMLElement).dataset.entryPath
            if (!path) continue
            if (observedEntry.isIntersecting) {
              if (!next.has(path)) {
                next.add(path)
                changed = true
              }
            } else if (next.delete(path)) {
              changed = true
            }
          }

          return changed ? next : current
        })
      },
      { threshold: 0.01 }
    )

    entries.forEach((entry) => observer.observe(entry))
    return () => observer.disconnect()
  }, [data])

  function toggle(path: string, checked: boolean) {
    if (mode === "single") {
      onValueChange(checked ? [path] : [])
      return
    }
    onValueChange(
      checked
        ? Array.from(new Set([...value, path]))
        : value.filter((item) => item !== path)
    )
  }

  function navigate(path = "") {
    clearAddressDebounce()
    setAddressTouched(false)
    setAddressRequestError(false)
    void load(path)
  }

  function submitAddress(value: string) {
    clearAddressDebounce()
    setAddressTouched(true)
    const normalized = normalizeBrowserPath(value, data?.roots ?? [])
    if (!normalized) return
    void load(normalized, "address")
  }

  function handleAddressChange(event: React.ChangeEvent<HTMLInputElement>) {
    const nextValue = event.target.value
    setAddressInput(nextValue)
    setAddressTouched(true)
    setAddressRequestError(false)
    setError("")
    clearAddressDebounce()

    const normalized = normalizeBrowserPath(nextValue, data?.roots ?? [])
    if (!normalized) return
    addressDebounceTimerRef.current = window.setTimeout(() => {
      addressDebounceTimerRef.current = null
      void load(normalized, "address")
    }, addressDebounceDelay)
  }

  const normalizedAddress = normalizeBrowserPath(
    addressInput,
    data?.roots ?? []
  )
  const addressInvalid =
    addressRequestError || (addressTouched && normalizedAddress === null)

  const activeRoot =
    data?.roots.find(
      (root) => data.path === root || data.path.startsWith(`${root}/`)
    ) ?? data?.roots[0]

  return (
    <div data-slot="file-browser" className="flex flex-col gap-4">
      <div
        data-slot="file-browser-toolbar"
        className="flex flex-wrap items-center gap-2"
      >
        <Select
          disabled={loading}
          items={(data?.roots ?? []).map((root) => ({
            label: root,
            value: root,
          }))}
          value={activeRoot}
          onValueChange={(root) => navigate(root ?? "")}
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
          onClick={() => navigate(data?.parent)}
        >
          <ChevronUpIcon data-icon="inline-start" />
          上一级
        </Button>
        <Button
          variant="ghost"
          size="sm"
          disabled={loading}
          onClick={() => navigate(data?.path)}
        >
          <RefreshCwIcon data-icon="inline-start" />
          刷新
        </Button>
        <span className="min-w-0 flex-1 truncate text-sm text-muted-foreground">
          {data?.path || "正在读取目录…"}
        </span>
      </div>

      <FieldGroup data-slot="file-browser-address" className="gap-0">
        <Field data-invalid={addressInvalid} data-disabled={!data || loading}>
          <FieldLabel htmlFor={addressInputId} className="sr-only">
            文件浏览器地址
          </FieldLabel>
          <Input
            id={addressInputId}
            data-slot="file-browser-address-input"
            value={addressInput}
            aria-label="文件浏览器地址"
            aria-invalid={addressInvalid}
            autoComplete="off"
            disabled={!data || loading}
            placeholder="输入扫描根目录内的绝对路径"
            spellCheck={false}
            onChange={handleAddressChange}
            onKeyDown={(event) => {
              if (event.key !== "Enter") return
              event.preventDefault()
              submitAddress(addressInput)
            }}
          />
        </Field>
      </FieldGroup>

      {error && (
        <div data-slot="file-browser-error">
          <ErrorAlert message={error} />
        </div>
      )}

      <div
        data-slot="file-browser-list"
        aria-busy={loading}
        className="relative min-h-64 rounded-xl border"
      >
        <div
          ref={listRef}
          data-slot="file-browser-list-viewport"
          className="flex min-h-64 flex-col overflow-auto rounded-[inherit]"
        >
          {data?.entries.length ? (
            data.entries.map((entry) => {
              const checked = value.includes(entry.path)
              const nameVisible = !loading && visibleEntries.has(entry.path)
              return (
                <div
                  key={entry.path}
                  data-slot="file-browser-entry"
                  data-entry-path={entry.path}
                  className="flex w-full min-w-[64rem] items-center gap-3 border-b px-3 py-2 last:border-b-0"
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
                      onClick={() => navigate(entry.path)}
                    >
                      <FolderIcon data-icon="inline-start" />
                      <MarqueeFilename
                        name={entry.name}
                        visible={nameVisible}
                      />
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
                      <MarqueeFilename
                        name={entry.name}
                        visible={nameVisible}
                      />
                    </div>
                  )}
                  <span
                    data-slot="file-browser-entry-path"
                    title={entry.path}
                    className="ml-auto min-w-0 flex-1 truncate text-xs text-muted-foreground"
                  >
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
        {loading && (
          <div
            data-slot="file-browser-loading"
            className="absolute inset-0 grid animate-in place-items-center rounded-[inherit] bg-black/10 duration-100 fade-in-0 supports-backdrop-filter:backdrop-blur-xs"
          >
            <div className="flex flex-col items-center gap-2 text-sm">
              <Spinner className="size-6" aria-label="正在读取目录内容" />
              <span>正在读取目录内容</span>
            </div>
          </div>
        )}
      </div>

      <div
        data-slot="file-browser-selection"
        className="flex min-h-7 flex-wrap gap-2"
      >
        {value.map((path) => (
          <Badge key={path} variant="secondary">
            {path}
          </Badge>
        ))}
      </div>
    </div>
  )
}

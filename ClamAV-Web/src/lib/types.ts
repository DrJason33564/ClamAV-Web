export type ScanAction = "warn" | "move" | "remove"

export type BrowseEntry = {
  name: string
  path: string
  is_dir: boolean
  size: number
  modified: string
}

export type BrowseResponse = {
  path: string
  parent?: string
  entries: BrowseEntry[]
  roots: string[]
}

export type StatusSource = {
  message?: string
  updated_at?: string
  clamd?: {
    status?: string
    last_checked_at?: string
    message?: string
  }
  scan?: {
    active_job_id?: string | null
    last_job_id?: string | null
    last_job_status?: string | null
    last_job_result?: string | null
  }
}

export type StatusResponse = {
  source: StatusSource | string
  ping: string
  ping_message: string
  checked_at: string
  first_run: "completed" | "not_completed"
  is_timedock: boolean
}

export type UserRole = "admin" | "user"

export type CurrentUser = {
  username: string
  role: UserRole
  timedock_account: string
}

export type FirstRunResponse = {
  first_run: "completed" | "not_completed"
}

export type AuthResponse = CurrentUser & { status?: string; message?: string }

export type AdminUser = CurrentUser & {
  status: "active" | "disabled" | "deleting"
  password_set: boolean
  created_at: number
  updated_at: number
}

export type AdminUsersResponse = {
  status: string
  users: AdminUser[]
}

export type ServiceConfig = {
  history_index_refresh_interval: number
  web_firstrun_completed: number
}

export type ClamAVPowerResponse = {
  status: "awake" | "sleeping"
  message: string
}

export type CronRule = {
  id: string
  enabled: boolean
  minute: string
  hour: string
  day: string
  month: string
  weekday: string
  target: string
  action: ScanAction
  wake: boolean
}

export type CronRulesResponse = {
  rules: CronRule[]
  message?: string
}

export type QueueItem = {
  id: string
  status: string
  job_ids: string[]
  targets: string[]
  action: ScanAction
  started_at?: string | null
  queue_number: number
}

export type WhitelistEntry = {
  path: string
  line: number
}

export type WhitelistResponse = {
  status: string
  entries?: WhitelistEntry[]
  message?: string
}

export type HistoryItem = {
  id: string
  type: string
  date: string
  result: string | null
}

export type HistoryLookup = {
  lookup_id: string
  status: "pending" | "success" | "failed"
  results?: HistoryItem[]
  total: number
  error?: string
}

export type DetectionDetail = {
  job_id: string
  detections: Array<{
    source_file: string
    detection_reason: string
  }>
  original: string
  log: string
}

export type QuarantineSubject = {
  name: string
  source_file: string
}

export type QuarantineLookup = {
  lookup_id: string
  status: "pending" | "success" | "failed"
  subjects?: QuarantineSubject[]
  error?: string
}

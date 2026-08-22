import * as React from "react"
import {
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
  UserRoundCogIcon,
} from "lucide-react"

import { PageLayout } from "@/components/page-layout"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { toast } from "@/components/ui/toast"
import { api, jsonRequest } from "@/lib/api"
import { errorMessage } from "@/lib/format"
import type {
  AdminUser,
  AdminUsersResponse,
  CurrentUser,
  UserRole,
} from "@/lib/types"

const roleItems = [
  { label: "普通用户", value: "user" },
  { label: "管理员", value: "admin" },
]
const statusItems = [
  { label: "启用", value: "active" },
  { label: "禁用", value: "disabled" },
]

type Draft = {
  username: string
  password: string
  role: UserRole
  status: "active" | "disabled"
  timedock_account: string
}
const emptyDraft: Draft = {
  username: "",
  password: "",
  role: "user",
  status: "active",
  timedock_account: "",
}

function formatDate(value: number) {
  return value
    ? new Intl.DateTimeFormat("zh-CN", {
        dateStyle: "medium",
        timeStyle: "short",
      }).format(value * 1000)
    : "—"
}

export function UsersPage({
  currentUser,
  isTimedock,
  onSessionChange,
}: {
  currentUser: CurrentUser
  isTimedock: boolean
  onSessionChange: () => Promise<void>
}) {
  const [users, setUsers] = React.useState<AdminUser[]>([])
  const [loading, setLoading] = React.useState(false)
  const [saving, setSaving] = React.useState(false)
  const [dialogOpen, setDialogOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<AdminUser | null>(null)
  const [draft, setDraft] = React.useState<Draft>(emptyDraft)
  const [confirm, setConfirm] = React.useState<{
    kind: "delete" | "password"
    user: AdminUser
  } | null>(null)

  const load = React.useCallback(async () => {
    setLoading(true)
    try {
      const result = await api<AdminUsersResponse>("/api/admin/users")
      setUsers(result.users ?? [])
    } catch (error) {
      toast.add({
        type: "error",
        title: "用户列表加载失败",
        description: errorMessage(error),
      })
    } finally {
      setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    queueMicrotask(() => void load())
  }, [load])

  function openCreate() {
    setEditing(null)
    setDraft(emptyDraft)
    setDialogOpen(true)
  }

  function openEdit(user: AdminUser) {
    setEditing(user)
    setDraft({
      username: user.username,
      password: "",
      role: user.role,
      status: user.status === "disabled" ? "disabled" : "active",
      timedock_account: user.timedock_account,
    })
    setDialogOpen(true)
  }

  async function save(event: React.FormEvent) {
    event.preventDefault()
    setSaving(true)
    try {
      const body: Record<string, string> = {
        role: draft.role,
      }
      if (isTimedock) body.timedock_account = draft.timedock_account
      if (draft.password) body.password = draft.password
      if (editing) body.status = draft.status
      await api(
        editing
          ? `/api/admin/users/${encodeURIComponent(editing.username)}`
          : "/api/admin/users",
        jsonRequest(
          editing ? "PATCH" : "POST",
          editing ? body : { ...body, username: draft.username }
        )
      )
      toast.add({
        type: "success",
        title: editing ? "用户已更新" : "用户已创建",
      })
      setDialogOpen(false)
      await load()
      if (editing?.username === currentUser.username) await onSessionChange()
    } catch (error) {
      toast.add({
        type: "error",
        title: "保存失败",
        description: errorMessage(error),
      })
    } finally {
      setSaving(false)
    }
  }

  async function runConfirmed() {
    if (!confirm) return
    setSaving(true)
    try {
      const url = `/api/admin/users/${encodeURIComponent(confirm.user.username)}`
      if (confirm.kind === "delete") await api(url, { method: "DELETE" })
      else await api(url, jsonRequest("PATCH", { password: "" }))
      toast.add({
        type: "success",
        title: confirm.kind === "delete" ? "用户已删除" : "密码已清除",
      })
      setConfirm(null)
      await load()
    } catch (error) {
      toast.add({
        type: "error",
        title: "操作失败",
        description: errorMessage(error),
      })
    } finally {
      setSaving(false)
    }
  }

  return (
    <PageLayout
      title="用户管理"
      description={
        isTimedock
          ? "创建账户并管理角色、状态与拾光坞账户绑定"
          : "创建账户并管理角色与状态"
      }
      icon={UserRoundCogIcon}
      actions={
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={loading}
            onClick={() => void load()}
          >
            <RefreshCwIcon data-icon="inline-start" />
            刷新
          </Button>
          <Button size="sm" onClick={openCreate}>
            <PlusIcon data-icon="inline-start" />
            创建用户
          </Button>
        </div>
      }
    >
      <Card size="sm">
        <CardHeader>
          <CardTitle>账户</CardTitle>
          <CardDescription>共 {users.length} 个账户</CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>用户</TableHead>
                <TableHead>角色</TableHead>
                <TableHead>状态</TableHead>
                {isTimedock && <TableHead>拾光坞账户</TableHead>}
                <TableHead>密码</TableHead>
                <TableHead>更新时间</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((user) => (
                <TableRow key={user.username}>
                  <TableCell className="font-medium">
                    {user.username}
                    {user.username === currentUser.username && (
                      <span className="ml-1 text-xs text-muted-foreground">
                        （当前）
                      </span>
                    )}
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant={user.role === "admin" ? "default" : "secondary"}
                    >
                      {user.role === "admin" ? "管理员" : "普通用户"}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant={
                        user.status === "active"
                          ? "success"
                          : user.status === "deleting"
                            ? "destructive"
                            : "secondary"
                      }
                    >
                      {user.status === "active"
                        ? "启用"
                        : user.status === "disabled"
                          ? "禁用"
                          : "删除中"}
                    </Badge>
                  </TableCell>
                  {isTimedock && (
                    <TableCell>{user.timedock_account || "未绑定"}</TableCell>
                  )}
                  <TableCell>
                    {user.password_set ? "已设置" : "未设置"}
                  </TableCell>
                  <TableCell>{formatDate(user.updated_at)}</TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        title="编辑"
                        disabled={user.status === "deleting"}
                        onClick={() => openEdit(user)}
                      >
                        <PencilIcon />
                        <span className="sr-only">编辑</span>
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={
                          !user.password_set ||
                          user.username === currentUser.username
                        }
                        onClick={() => setConfirm({ kind: "password", user })}
                      >
                        清除密码
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        title={user.status === "deleting" ? "重试删除" : "删除"}
                        disabled={user.username === currentUser.username}
                        onClick={() => setConfirm({ kind: "delete", user })}
                      >
                        <Trash2Icon />
                        <span className="sr-only">删除</span>
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {!loading && users.length === 0 && (
            <p className="py-8 text-center text-muted-foreground">暂无用户</p>
          )}
        </CardContent>
      </Card>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {editing ? `编辑 ${editing.username}` : "创建用户"}
            </DialogTitle>
            <DialogDescription>
              {editing
                ? "密码留空则不修改"
                : "密码可留空，未设置密码的账户不能登录"}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={save}>
            <FieldGroup>
              {!editing && (
                <Field>
                  <FieldLabel htmlFor="user-name">用户名</FieldLabel>
                  <Input
                    id="user-name"
                    required
                    autoFocus
                    value={draft.username}
                    onChange={(event) =>
                      setDraft({ ...draft, username: event.target.value })
                    }
                  />
                </Field>
              )}
              <Field>
                <FieldLabel htmlFor="user-password">
                  {editing ? "新密码（可选）" : "初始密码（可选）"}
                </FieldLabel>
                <Input
                  id="user-password"
                  type="password"
                  autoComplete="new-password"
                  value={draft.password}
                  onChange={(event) =>
                    setDraft({ ...draft, password: event.target.value })
                  }
                />
              </Field>
              <Field>
                <FieldLabel>角色</FieldLabel>
                <Select
                  items={roleItems}
                  value={draft.role}
                  onValueChange={(value) =>
                    setDraft({ ...draft, role: value as UserRole })
                  }
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      {roleItems.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {item.label}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </Field>
              {editing && (
                <Field>
                  <FieldLabel>状态</FieldLabel>
                  <Select
                    items={statusItems}
                    value={draft.status}
                    onValueChange={(value) =>
                      setDraft({ ...draft, status: value as Draft["status"] })
                    }
                  >
                    <SelectTrigger className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        {statusItems.map((item) => (
                          <SelectItem key={item.value} value={item.value}>
                            {item.label}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </Field>
              )}
              {isTimedock && (
                <Field>
                  <FieldLabel htmlFor="user-timedock">拾光坞账户</FieldLabel>
                  <Input
                    id="user-timedock"
                    value={draft.timedock_account}
                    onChange={(event) =>
                      setDraft({
                        ...draft,
                        timedock_account: event.target.value,
                      })
                    }
                  />
                  <FieldDescription>
                    为空时该用户无法访问扫描目录
                  </FieldDescription>
                </Field>
              )}
            </FieldGroup>
            <DialogFooter className="mt-5">
              <Button
                type="button"
                variant="outline"
                onClick={() => setDialogOpen(false)}
              >
                取消
              </Button>
              <Button
                type="submit"
                disabled={saving || (!editing && !draft.username)}
              >
                {saving && <Spinner data-icon="inline-start" />}保存
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={Boolean(confirm)}
        onOpenChange={(open) => !open && setConfirm(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirm?.kind === "delete" ? "删除用户？" : "清除登录密码？"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm?.kind === "delete"
                ? `将彻底删除 ${confirm.user.username} 及其任务、规则和隔离数据，此操作不可撤销`
                : `${confirm?.user.username} 将无法登录，现有会话也会失效`}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={saving}
              onClick={() => void runConfirmed()}
            >
              {saving && <Spinner data-icon="inline-start" />}
              {confirm?.kind === "delete" ? "确认删除" : "清除密码"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </PageLayout>
  )
}

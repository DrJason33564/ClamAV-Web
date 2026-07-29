import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import type { ScanAction } from "@/lib/types"

const actions = [
  { label: "仅告警", value: "warn" },
  { label: "移动到隔离区", value: "move" },
  { label: "直接删除", value: "remove" },
] satisfies Array<{ label: string; value: ScanAction }>

export function ActionSelect({
  value,
  onValueChange,
}: {
  value: ScanAction
  onValueChange: (value: ScanAction) => void
}) {
  return (
    <Select
      items={actions}
      value={value}
      onValueChange={(nextValue) => onValueChange(nextValue as ScanAction)}
    >
      <SelectTrigger className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          {actions.map((action) => (
            <SelectItem key={action.value} value={action.value}>
              {action.label}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  )
}

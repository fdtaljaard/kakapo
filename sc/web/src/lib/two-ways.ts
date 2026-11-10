import type { Finding, Fix } from "@/api"

// A finding with two ways out (BAK02: schedule the log backups, or switch to SIMPLE recovery) has one
// Apply for each, named for what it does; each applies its own fix. undefined for any other finding.
export function twoWaysChoices(
  f: Pick<Finding, "fixId" | "altFixId">,
  fixes?: Pick<Fix, "id" | "title" | "target">[],
): { label: string; options?: Record<string, string>; fixId: string }[] | undefined {
  if (!f.fixId || !f.altFixId) return undefined
  return [f.fixId, f.altFixId].map((id) => ({ label: twoWaysLabel(id, fixes), fixId: id }))
}

// "Schedule log backups", "Switch to SIMPLE".
export function twoWaysLabel(fixId: string, fixes?: Pick<Fix, "id" | "title" | "target">[]): string {
  const fix = fixes?.find((x) => x.id === fixId)
  if (fixId === "recovery-model") return `Switch to ${fix?.target ?? "SIMPLE"}`
  if (fixId === "backup-schedule") return "Schedule log backups"
  return fix?.title ?? "Apply"
}

import { HardDrive } from "lucide-react"

import type { RoomLine, StorageDrives } from "@/api"
import { cn } from "@/lib/utils"

// The room line (docs/phase5l-contract.md): one bar per drive a change uses, in use now (grey), this change
// (the state's colour) and free after; the sentence, then what was counted and how. The same bar leads the
// Storage card (DriveBars), so the room a fix needs reads the same everywhere.

const stateLabel: Record<RoomLine["state"], string> = {
  fits: "Fits",
  tight: "Tight",
  nofit: "Does not fit",
  unknown: "Not readable",
}

const tagClass: Record<RoomLine["state"], string> = {
  fits: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
  tight: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
  nofit: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300",
  unknown: "bg-muted text-muted-foreground",
}

const changeClass: Record<RoomLine["state"], string> = {
  fits: "bg-sky-600",
  tight: "bg-amber-500",
  nofit: "bg-red-600",
  unknown: "bg-muted-foreground",
}

// A bar of a drive: used now, a coloured part, and the rest free. Widths in % of the total; the coloured part
// is at least a sliver so a small change still shows.
export function DriveBar({ usedPct, partPct, partClass }: { usedPct: number; partPct: number; partClass: string }) {
  const used = Math.max(0, Math.min(100, usedPct))
  const part = partPct > 0 ? Math.max(0.6, Math.min(100 - used, partPct)) : 0
  return (
    <div className="flex h-3 w-full overflow-hidden rounded-sm border bg-muted/40" aria-hidden>
      <span className="h-full bg-zinc-400 dark:bg-zinc-500" style={{ width: `${used}%` }} />
      {part > 0 && <span className={cn("h-full", partClass)} style={{ width: `${part}%` }} />}
    </div>
  )
}

export function RoomLines({ room }: { room?: RoomLine[] }) {
  if (!room || room.length === 0) return null
  return (
    <div className="grid gap-2" aria-label="Room on the drive">
      {room.map((l) => (
        <RoomLineView key={l.drive} line={l} />
      ))}
    </div>
  )
}

function RoomLineView({ line }: { line: RoomLine }) {
  const readable = line.state !== "unknown"
  const total = line.totalMB
  const hasBar = readable && total > 0
  const usedPct = hasBar ? ((total - line.freeMB) / total) * 100 : 0
  const partPct = hasBar ? (Math.min(line.needMB, line.freeMB > 0 ? line.needMB : 0) / total) * 100 : 0
  return (
    <div className={cn("rounded-md border px-3 py-2.5 grid gap-1.5", line.state === "nofit" && "border-red-400 dark:border-red-800")}>
      <div className="flex items-baseline justify-between gap-3 text-sm">
        <span className="flex items-center gap-1.5 font-medium min-w-0">
          <HardDrive className="size-4 shrink-0 self-center text-muted-foreground" aria-hidden />
          <span className="break-all">Room on {line.drive}</span>
        </span>
        <span className={cn("rounded px-2 py-0.5 text-xs whitespace-nowrap", tagClass[line.state])}>{stateLabel[line.state]}</span>
      </div>
      {hasBar && <DriveBar usedPct={usedPct} partPct={partPct} partClass={changeClass[line.state]} />}
      <p className="text-sm">{line.sentence}</p>
      <ul className="grid gap-0.5 text-xs text-muted-foreground">
        {line.parts.map((p, i) => (
          <li key={i}>
            {p.label}: {p.basis}
            {p.weak && " (weak estimate)"}
          </li>
        ))}
      </ul>
    </div>
  )
}

// Phase 5l: the Storage card's answer first (docs/phase5l-contract.md part 5): the sentence, then one bar per
// drive, everything else grey and this database's files coloured, labelled with what is on it.
const mb = (v: number) =>
  v < 1024 ? `${Math.round(v).toLocaleString()} MB` : v < 100 * 1024 ? `${(v / 1024).toFixed(1)} GB` : `${Math.round(v / 1024).toLocaleString()} GB`

export function StorageDrivesBlock({ drives, database }: { drives: StorageDrives; database: string }) {
  return (
    <section className="rounded-lg border p-4 grid gap-3" aria-label="Room on the drives">
      <p className="text-lg font-medium">{drives.sentence}</p>
      {drives.drives.map((d) => {
        const pct = Math.round((d.freeMB * 100) / d.totalMB)
        return (
          <div key={d.drive} className="grid gap-1.5">
            <div className="flex items-baseline justify-between gap-3 text-sm">
              <span className="flex items-center gap-1.5 font-medium min-w-0">
                <HardDrive className="size-4 shrink-0 self-center text-muted-foreground" aria-hidden />
                {d.drive} <span className="font-normal text-muted-foreground">{d.holds.join(", ")}</span>
              </span>
              <span className={cn("rounded px-2 py-0.5 text-xs whitespace-nowrap", tagClass[d.state])}>{pct} % free</span>
            </div>
            <DriveBar
              usedPct={((d.totalMB - d.freeMB - d.databaseMB) * 100) / d.totalMB}
              partPct={(d.databaseMB * 100) / d.totalMB}
              partClass="bg-violet-500"
            />
            <p className="text-xs text-muted-foreground">
              {mb(d.freeMB)} free of {mb(d.totalMB)}.{d.databaseMB > 0 && ` ${database}'s files: ${mb(d.databaseMB)}.`}
              {d.daysLeft !== undefined
                ? ` At its current rate it fills in about ${d.daysLeft} days (STO12).`
                : " Trend: needs samples a day or more apart (STO12)."}
            </p>
          </div>
        )
      })}
    </section>
  )
}

// Dev only: the What changed? screen (docs/phase5w-contract.md) with made-up data: a cap removed outside
// kakapo, kakapo's own cost threshold change, a reorganise that never ended, and a procedure seen in the trace.
import type { ChangeRow, ChangesPage, MemoryPoint } from "./api-changes"
import type { FeatureMock } from "./api.mock"

const hour = 3_600_000
const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()

function points(): MemoryPoint[] {
  const out: MemoryPoint[] = []
  for (let i = 14 * 24; i >= 0; i -= 6) {
    if (i < 9 * 24 && i > 5 * 24) continue // kakapo not opened for a few days
    const capped = i > 4 * 24
    out.push({ at: iso(i * hour), sqlMB: capped ? 10_000 + (i % 5) * 40 : 30_000 - i * 20, targetMB: capped ? 10_240 : 31_000, capMB: capped ? 10_240 : undefined })
  }
  return out
}

const rows: ChangeRow[] = [
  {
    key: "aR1", at: iso(2 * hour), flag: "grey", rule: "kakapo-cache", headline: "Reorganise the selected indexes on WarehouseLive",
    tell: "Fills the cache with that table. Normal, unless pressure follows.", who: "Set by kakapo: DOMAIN\\consultant on LAPTOP-7",
    byKakapo: true, source: "kakapo", database: "WarehouseLive", screens: ["memory"], outcome: "unknown",
    statement: "ALTER INDEX [IX_Pick] ON [dbo].[Pick] REORGANIZE;", line: "",
  },
  {
    key: "s1", from: iso(4 * 24 * hour + 3 * hour), to: iso(4 * 24 * hour), flag: "red", rule: "memory-cap-raised",
    headline: "max server memory (MB) changed from 10240 to no cap",
    tell: "SQL Server can take more RAM, and Windows and other services can be squeezed.",
    who: "Changed outside kakapo. SQL Server's trace no longer holds who.", byKakapo: false, source: "baseline", screens: ["memory"],
    before: "10240", after: "no cap", item: "config/max server memory (MB)", line: "",
  },
  {
    key: "s2", at: iso(6 * 24 * hour), flag: "amber", rule: "parallelism", headline: "cost threshold for parallelism changed from 5 to 50",
    tell: "Query plans change, so CPU and speed can move.", who: "Set by kakapo: DOMAIN\\consultant on LAPTOP-7", byKakapo: true,
    source: "kakapo", screens: ["memory"], before: "5", after: "50", outcome: "verified", result: "50, verified",
    statement: "EXEC sys.sp_configure N'cost threshold for parallelism', 50;\nRECONFIGURE;", line: "",
  },
  {
    key: "c9", at: iso(10 * 24 * hour), headline: "Procedure dbo.GetPicks altered by DOMAIN\\dev on BUILD01 (SSMS)",
    who: "By DOMAIN\\dev on BUILD01 (SSMS), from SQL Server's trace", byKakapo: false, source: "trace", database: "WarehouseLive", screens: [], line: "",
  },
]

export const mockChanges: FeatureMock = async (method, path) => {
  const m = path.match(/^\/api\/servers\/(\d+)\/changes(?:\?(.*))?$/)
  if (method !== "GET" || !m) return undefined
  const q = new URLSearchParams(m[2] ?? "")
  const who = q.get("who")
  const flagged = q.get("flagged") === "1"
  const list = rows
    .filter((r) => (who === "kakapo" ? r.byKakapo : who === "outside" ? !r.byKakapo : true))
    .filter((r) => !flagged || r.flag === "red" || r.flag === "amber")
    .map((r) => ({ ...r, line: r.headline }))
  const problem = q.get("problemAt")
  const page: ChangesPage = {
    serverName: "SQL1",
    verdict: problem
      ? `3 changes in the 7 days before ${problem.replace("T", " ")} (server time), 1 of them red: max server memory (MB) changed from 10240 to no cap.`
      : "Since kakapo started watching this server on 29 Oct 2026: 4 changes, 2 of them by kakapo.",
    tone: "red",
    problemAt: problem ? new Date(problem).toISOString() : undefined,
    problemLabel: problem ? `${problem.replace("T", " ")} (server time)` : undefined,
    live: true,
    notes: ["kakapo has watched this server's settings since 29 Oct 2026; changes before that come only from kakapo's own log and SQL Server's own history."],
    memory: { points: points(), snapshots: [{ id: 3, at: iso(3 * 24 * hour) }], gaps: ["kakapo was not opened between 3 Nov and 7 Nov, so there is no reading in between."] },
    rows: list,
    page: 0,
    pageSize: 100,
    more: false,
    listed: list.length,
    counts: { all: rows.length, kakapo: 2, outside: 2, flagged: 2 },
    copy: list.map((r) => r.line).join("\n"),
  }
  return page
}

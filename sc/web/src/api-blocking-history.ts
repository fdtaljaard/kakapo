// Phase 5d: BLK01, a week of blocking history from the system_health session (docs/phase5d-contract.md).
// The types live in api.ts with CardDetail, which carries the last stored history.

import { request, type BlockingHistory } from "./api"

export const blockingHistoryDays = 7

export const blockingHistoryApi = {
  // Reads the system_health session now and stores the history. 400 for days outside 1 to 7, 404 for an
  // unknown server or database, 502 when the session cannot be read (the message says why, e.g. the login
  // needs VIEW SERVER STATE).
  read: (serverId: number, database: string, days = blockingHistoryDays) =>
    request<BlockingHistory>(
      `/api/servers/${serverId}/databases/${encodeURIComponent(database)}/cards/performance/blocking-history`,
      { method: "POST", body: JSON.stringify({ days }) },
    ),
}

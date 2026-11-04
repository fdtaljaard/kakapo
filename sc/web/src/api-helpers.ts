// Phase 5e: kakapo's installed helpers (docs/phase5e-contract.md). The instance page's "Installed helpers"
// row reads where they stand here; its Create and Remove go through api.previewFix / api.applyFix with the
// fix ids below, on the database the answer names. The types live in api.ts.

import { request, type InstalledHelpers } from "./api"

export const installXESessionFixId = "install-xe-session"
export const removeXESessionFixId = "remove-xe-session"

export const helpersApi = {
  // Read live; nothing is stored. 404 for an unknown server, 502 when the server cannot be read.
  read: (serverId: number) => request<InstalledHelpers>(`/api/servers/${serverId}/helpers`),
}

// The routes below Home (docs/phase2-contract.md, revisions 2 and 3). Database names go through
// encodeURIComponent; react-router decodes them again in useParams.

export const instancePath = (serverId: number) => `/instances/${serverId}`

// The edit form for the registration (revision 3).
export const editInstancePath = (serverId: number) => `${instancePath(serverId)}/edit`

export const databasePagePath = (serverId: number, database: string) =>
  `${instancePath(serverId)}/databases/${encodeURIComponent(database)}`

export const cardPath = (serverId: number, database: string, card: string) =>
  `${databasePagePath(serverId, database)}/cards/${card}`

// Phase 4c (docs/phase4c-contract.md): the investigation pages under a database.
export const queryPath = (serverId: number, database: string, hash: string) =>
  `${databasePagePath(serverId, database)}/queries/${encodeURIComponent(hash)}`

export const blockingPath = (serverId: number, database: string) => `${databasePagePath(serverId, database)}/blocking`

// Location state a Home tile sets, so the picker may go straight into the remembered database.
// A direct URL to the picker carries no state and always shows the picker.
export interface FromHome {
  fromHome: true
}

export function cameFromHome(state: unknown): boolean {
  return typeof state === "object" && state !== null && (state as Partial<FromHome>).fromHome === true
}

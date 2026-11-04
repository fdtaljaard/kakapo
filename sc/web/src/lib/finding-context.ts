import { createContext } from "react"

import type { HandoverContext } from "@/lib/handover"

// The context a FindingCard puts in its Copy block when it is not given one: the card page provides it, so
// the findings drawn inside the panels (STO10, DLK01) carry the server, database, as-of and build too.
export const FindingContext = createContext<HandoverContext | null>(null)

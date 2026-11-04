import { useState } from "react"
import { CheckCircle2, Database, Loader2, XCircle } from "lucide-react"

import { api, type AuthType, type ConnectionInfo, type Server, type ServerInput } from "@/api"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"

const empty: ServerInput = {
  name: "",
  host: "",
  auth: "windows",
  username: "",
  password: "",
  database: "",
  encrypt: false,
  trustCert: false,
}

const eyebrow: Record<SetupMode, string> = { "first-run": "First run", add: "Add server", edit: "Instance" }

type Outcome =
  | { kind: "idle" }
  | { kind: "ok"; info: ConnectionInfo }
  | { kind: "error"; message: string }

// What the form is for: the first registration, "Add server", or editing a registration.
export type SetupMode = "first-run" | "add" | "edit"

// The registration as the form shows it. The password is never returned, so it starts empty; for a
// SQL login an empty password means "keep the stored one".
function formFromServer(server: Server): ServerInput {
  return {
    name: server.name,
    host: server.host,
    auth: server.auth,
    username: server.username ?? "",
    password: "",
    database: server.database ?? "",
    encrypt: server.encrypt,
    trustCert: server.trustCert,
  }
}

// The server form. On first run it is all there is; from "Add server" it gets a Cancel (onCancel).
// With `server` it edits that registration: pre-filled, Test sends its id, Save is a PUT.
export function SetupScreen({
  onSaved,
  onCancel,
  server,
}: {
  onSaved: (server: Server) => void
  onCancel?: () => void
  server?: Server
}) {
  const mode: SetupMode = server ? "edit" : onCancel ? "add" : "first-run"
  const [form, setForm] = useState<ServerInput>(() => (server ? formFromServer(server) : empty))
  const [busy, setBusy] = useState<"test" | "save" | null>(null)
  const [outcome, setOutcome] = useState<Outcome>({ kind: "idle" })

  const set = <K extends keyof ServerInput>(key: K, value: ServerInput[K]) =>
    setForm((f) => ({ ...f, [key]: value }))

  // Editing a SQL login: an empty password keeps the stored one, for the test as for the save.
  const keepsPassword = mode === "edit" && server?.auth === "sql" && form.auth === "sql"

  async function test() {
    setBusy("test")
    try {
      const info = await api.testConnection(server ? { ...form, id: server.id } : form)
      setOutcome({ kind: "ok", info })
    } catch (e) {
      setOutcome({ kind: "error", message: (e as Error).message })
    } finally {
      setBusy(null)
    }
  }

  async function save() {
    setBusy("save")
    try {
      const saved = server ? await api.updateServer(server.id, form) : await api.registerServer(form)
      onSaved(saved)
    } catch (e) {
      setOutcome({ kind: "error", message: (e as Error).message })
    } finally {
      setBusy(null)
    }
  }

  const canSubmit = form.host.trim() !== "" && (form.auth === "windows" || form.username.trim() !== "")

  return (
    <main className="min-h-svh flex items-center justify-center p-6 bg-background">
      <Card className="w-full max-w-lg">
        <CardHeader>
          <div className="flex items-center gap-2 text-muted-foreground mb-1">
            <Database className="size-4" />
            <span className="text-xs font-medium uppercase tracking-wide">{eyebrow[mode]}</span>
          </div>
          <CardTitle className="break-words">
            {server ? `Edit ${server.name}` : "Which SQL Server do you want to monitor?"}
          </CardTitle>
          <CardDescription>
            {mode === "edit"
              ? "Save tests the connection first; if it fails, nothing is changed."
              : "Type the server as you would in SSMS. Nothing is installed on it; kakapo only reads."}
          </CardDescription>
        </CardHeader>

        <CardContent className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="host">Server</Label>
            <Input
              id="host"
              placeholder="CLIENTSQL01\WMS or 172.20.1.100,1433"
              value={form.host}
              onChange={(e) => set("host", e.target.value)}
              autoFocus={mode !== "edit"}
            />
          </div>

          <div className="grid gap-2">
            <Label htmlFor="name">
              Display name <span className="text-muted-foreground">(optional)</span>
            </Label>
            <Input
              id="name"
              placeholder="Client A production"
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </div>

          <div className="grid gap-2">
            <Label>Authentication</Label>
            <Select value={form.auth} onValueChange={(v) => set("auth", v as AuthType)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="windows">Windows authentication (current user)</SelectItem>
                <SelectItem value="sql">SQL Server login</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {form.auth === "sql" && (
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="username">User name</Label>
                <Input id="username" value={form.username} onChange={(e) => set("username", e.target.value)} />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="password">Password</Label>
                <Input
                  id="password"
                  type="password"
                  placeholder={keepsPassword ? "unchanged" : undefined}
                  autoComplete="new-password"
                  value={form.password}
                  onChange={(e) => set("password", e.target.value)}
                />
              </div>
            </div>
          )}

          <div className="grid gap-2">
            <Label htmlFor="database">
              Database <span className="text-muted-foreground">(optional)</span>
            </Label>
            <Input
              id="database"
              placeholder="GraniteLive"
              value={form.database}
              onChange={(e) => set("database", e.target.value)}
            />
          </div>

          <div className="flex flex-wrap gap-6 pt-1">
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={form.encrypt} onCheckedChange={(v) => set("encrypt", v === true)} />
              Encrypt connection
            </label>
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={form.trustCert}
                disabled={!form.encrypt}
                onCheckedChange={(v) => set("trustCert", v === true)}
              />
              Trust server certificate
            </label>
          </div>

          {outcome.kind === "ok" && (
            <Alert>
              <CheckCircle2 className="size-4" />
              <AlertTitle>Connected to {outcome.info.serverName}</AlertTitle>
              <AlertDescription>
                <div>{outcome.info.version}</div>
                <div className="text-muted-foreground">
                  {outcome.info.edition}, build {outcome.info.productVersion} {outcome.info.productLevel}
                </div>
              </AlertDescription>
            </Alert>
          )}
          {outcome.kind === "error" && (
            <Alert variant="destructive">
              <XCircle className="size-4" />
              <AlertTitle>Could not connect</AlertTitle>
              <AlertDescription className="break-words">{outcome.message}</AlertDescription>
            </Alert>
          )}
        </CardContent>

        <CardFooter className="flex flex-wrap justify-end gap-2">
          {onCancel && (
            <Button variant="ghost" className="mr-auto" onClick={onCancel} disabled={busy !== null}>
              Cancel
            </Button>
          )}
          <Button variant="outline" onClick={test} disabled={!canSubmit || busy !== null}>
            {busy === "test" && <Loader2 className="size-4 animate-spin" />}
            Test connection
          </Button>
          <Button onClick={save} disabled={!canSubmit || busy !== null}>
            {busy === "save" && <Loader2 className="size-4 animate-spin" />}
            Save
          </Button>
        </CardFooter>
      </Card>
    </main>
  )
}

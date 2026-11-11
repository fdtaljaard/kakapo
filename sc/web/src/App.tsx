import { useEffect } from "react"
import { BrowserRouter, Navigate, Route, Routes, useNavigate } from "react-router-dom"

import { Splash } from "@/components/splash"
import { CardScreen } from "@/screens/card"
import { DatabaseScreen } from "@/screens/database"
import { EditInstanceScreen } from "@/screens/edit"
import { HomeScreen } from "@/screens/home"
import { IndexesScreen } from "@/screens/indexes"
import { InstanceScreen } from "@/screens/instance"
import { BlockingScreen } from "@/screens/blocking"
import { QueryScreen } from "@/screens/query"
import { TuningScreen } from "@/screens/tuning"
import { SetupScreen } from "@/screens/setup"
import { instancePath, type FromHome } from "@/lib/paths"

export default function App() {
  // Keep one event stream open for as long as the page is shown. The exe stops a few
  // seconds after the last window drops it, so closing the window closes the app.
  useEffect(() => {
    const events = new EventSource("/api/events")
    return () => events.close()
  }, [])

  return (
    <BrowserRouter>
      <Splash />
      <Routes>
        <Route path="/" element={<HomeScreen />} />
        <Route path="/servers/new" element={<NewServer />} />
        <Route path="/instances/:id" element={<InstanceScreen />} />
        <Route path="/instances/:id/edit" element={<EditInstanceScreen />} />
        <Route path="/instances/:id/databases/:db" element={<DatabaseScreen />} />
        <Route path="/instances/:id/databases/:db/cards/:card" element={<CardScreen />} />
        <Route path="/instances/:id/databases/:db/queries/:hash" element={<QueryScreen />} />
        <Route path="/instances/:id/databases/:db/indexes" element={<IndexesScreen />} />
        <Route path="/instances/:id/databases/:db/blocking" element={<BlockingScreen />} />
        <Route path="/instances/:id/databases/:db/tuning" element={<TuningScreen />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  )
}

function NewServer() {
  const navigate = useNavigate()
  // A registration that names a database opens straight into it, as a Home tile does.
  const fromHome: FromHome = { fromHome: true }
  return (
    <SetupScreen
      onSaved={(server) => navigate(instancePath(server.id), { state: fromHome })}
      onCancel={() => navigate("/")}
    />
  )
}

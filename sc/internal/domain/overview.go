package domain

import (
	"fmt"
	"math"
	"time"
)

// CardID names one of the six cards on the database page (docs/landing-page.md).
type CardID string

const (
	CardBackups     CardID = "backups"
	CardSetup       CardID = "setup"
	CardStorage     CardID = "storage"
	CardUpkeep      CardID = "upkeep"
	CardPerformance CardID = "performance"
	CardDesign      CardID = "design"
)

// Card colours. Each means one thing everywhere: act today, something to do, no problem found,
// not checked yet or could not be read.
const (
	StatusGreen = "green"
	StatusAmber = "amber"
	StatusRed   = "red"
	StatusGrey  = "grey"
)

// Fact tones.
const (
	ToneNormal = "normal"
	ToneBad    = "bad"
	ToneWorse  = "worse"
)

// CardDef is what a card is, before anything is read: its title, its question, and the line a
// grey card shows about what it will show once its checks exist.
type CardDef struct {
	ID       CardID
	Title    string
	Question string
	Note     string
}

// CardDefs are the six cards, in the order the page shows them.
var CardDefs = []CardDef{
	{CardBackups, "Backups and data safety", "Could you lose data?",
		"Full and log backups, CHECKDB, damaged pages and the worst-case data loss per database."},
	{CardSetup, "Setup and configuration", "Is it set up the way it should be?", ""},
	{CardStorage, "Storage and space", "Will it run out of room?",
		"Free disk, file growth, log size, tempdb and the tables that only grow."},
	{CardUpkeep, "Upkeep", "Is someone looking after it?",
		"SQL Agent, failed jobs, index and statistics maintenance, and who is told about errors."},
	{CardPerformance, "Performance", "Is it slow, and why?",
		"Waits in plain English, CPU pressure, blocking, deadlocks and the costliest queries."},
	{CardDesign, "Database design", "Is the database built well?",
		"Indexes, heaps, the tables everyone waits on, and the application's stored procedures, per database."},
}

// Fact is one labelled value on a card.
type Fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Tone  string `json:"tone"` // normal, bad, worse
}

// Card is one question on the database page, answered.
type Card struct {
	ID          CardID     `json:"id"`
	Title       string     `json:"title"`
	Question    string     `json:"question"`
	Status      string     `json:"status"` // green, amber, red, grey
	StatusText  string     `json:"statusText"`
	Verdict     string     `json:"verdict"` // "" when grey
	Facts       []Fact     `json:"facts"`
	ToDo        int        `json:"toDo"`        // findings that need an action
	ForClientIT int        `json:"forClientIT"` // of those, for the client's IT
	Fixable     int        `json:"fixable"`     // of those, the app can apply
	AsOf        *time.Time `json:"asOf"`        // nil when grey
	Note        string     `json:"note"`
}

// InstanceInfo is the instance part of the database page header.
type InstanceInfo struct {
	ServerID       int64     `json:"serverId"`
	Name           string    `json:"name"` // the registered display name
	Host           string    `json:"host"`
	ServerName     string    `json:"serverName"` // @@SERVERNAME
	Edition        string    `json:"edition"`
	ProductVersion string    `json:"productVersion"`
	ProductLevel   string    `json:"productLevel"`
	ProductName    string    `json:"productName"` // "SQL Server 2019"
	StartedAt      time.Time `json:"startedAt"`   // UTC
	UptimeDays     float64   `json:"uptimeDays"`  // one decimal
	DatabaseCount  int       `json:"databaseCount"`
}

// ChangeEntry is one row of the change log as the database page shows it.
type ChangeEntry struct {
	ID          int64     `json:"id"`
	ServerID    int64     `json:"serverId"`
	Database    string    `json:"database"`
	ChangedAt   time.Time `json:"changedAt"`
	Object      string    `json:"object"`
	ChangeType  string    `json:"changeType"`
	Description string    `json:"description"`
	Result      string    `json:"result"`
}

// Entry is the change as the database page shows it, without the statements.
func (c Change) Entry() ChangeEntry {
	return ChangeEntry{ID: c.ID, ServerID: c.ServerID, Database: c.Database, ChangedAt: c.ChangedAt.UTC(),
		Object: c.Object, ChangeType: c.ChangeType, Description: c.Description, Result: c.Result}
}

// Overview is the whole database page in one read: the instance header, the chosen database and
// the six cards about it.
type Overview struct {
	Instance      InstanceInfo   `json:"instance"`
	Database      DatabaseStatus `json:"database"`
	Cards         []Card         `json:"cards"`         // always six, in CardDefs order
	RecentChanges []ChangeEntry  `json:"recentChanges"` // newest first
	CheckedAt     time.Time      `json:"checkedAt"`
}

// UptimeDays is the time since start in days, to one decimal.
func UptimeDays(startedAt, now time.Time) float64 {
	d := now.Sub(startedAt).Hours() / 24
	if d < 0 {
		return 0
	}
	return math.Round(d*10) / 10
}

// BuildCards answers the six cards about one database from what was read for them: Setup,
// Backups, Storage, Upkeep and Performance (each grey, "Not checked yet", when nil); the Storage
// card with storageTrend, the stored samples STO12 forecasts from (or nil). Database design is grey,
// "Not checked yet".
func BuildCards(setup *SetupFacts, backups *BackupFacts, storage *StorageFacts, storageTrend *DiskTrend, upkeep *UpkeepFacts,
	performance *PerformanceFacts) []Card {
	cards := make([]Card, 0, len(CardDefs))
	for _, def := range CardDefs {
		switch {
		case def.ID == CardSetup && setup != nil:
			c, _, _ := BuildSetupCard(*setup, def)
			cards = append(cards, c)
		case def.ID == CardBackups && backups != nil:
			c, _, _ := BuildBackupsCard(*backups, def)
			cards = append(cards, c)
		case def.ID == CardStorage && storage != nil:
			// The last CHECKDB room result changes only STO10's line in the checks, never the card.
			c, _, _ := BuildStorageCard(*storage, def, nil, storageTrend)
			cards = append(cards, c)
		case def.ID == CardUpkeep && upkeep != nil:
			c, _, _ := BuildUpkeepCard(*upkeep, def)
			cards = append(cards, c)
		case def.ID == CardPerformance && performance != nil:
			// The last deadlock report and blocking history change only DLK01's and BLK01's lines in the checks, never the card.
			c, _, _ := BuildPerformanceCard(*performance, def, nil, nil)
			cards = append(cards, c)
		default:
			cards = append(cards, GreyCard(def))
		}
	}
	return cards
}

// FindCardDef returns the definition of a card by its id.
func FindCardDef(id CardID) (CardDef, bool) {
	for _, def := range CardDefs {
		if def.ID == id {
			return def, true
		}
	}
	return CardDef{}, false
}

// GreyCard is a card whose checks do not exist yet: "Not checked yet", with the line about what
// it will show.
func GreyCard(def CardDef) Card {
	return Card{ID: def.ID, Title: def.Title, Question: def.Question, Status: StatusGrey,
		StatusText: "Not checked yet", Facts: []Fact{}, Note: def.Note}
}

// unavailableReason says why Query Store cannot be changed on a database from here.
func unavailableReason(db DatabaseStatus) string {
	switch {
	case db.IsSecondaryReplica:
		return db.Name + " is a secondary replica here; Query Store is set on the primary."
	case db.State != "ONLINE":
		return fmt.Sprintf("%s is %s, not online, so Query Store cannot be changed.", db.Name, db.State)
	case db.ReadOnly:
		return db.Name + " is read-only, so Query Store cannot be changed."
	case db.QueryStore != nil && db.QueryStore.ReadonlyReasonText != "":
		return "Query Store on " + db.Name + " is read-only: " + db.QueryStore.ReadonlyReasonText + "."
	}
	return "Query Store on " + db.Name + " cannot be changed from here."
}

func tone(bad bool) string {
	if bad {
		return ToneBad
	}
	return ToneNormal
}

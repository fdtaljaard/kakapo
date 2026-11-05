package domain

import "sort"

// Red flags (docs/phase5i-contract.md, section 3): the findings the cards already raise that are badly
// wrong right now, at the severities the cards already give them. No new check: a listed check at High
// (or Critical), and any Critical finding of any card.

// redFlagRows are the contract's table, in its order: blocking now; pressure now; deadlocks (DLK01 and
// BLK01 from the stored reports, as the card); waits that stop work; space; backups (MNT02 is High only
// for a failed backup job). Any other Critical finding is a last row of its own.
var redFlagRows = [][]string{
	{"ACT01"},
	{"PRS01"},
	{"WRK06", "DLK01", "BLK01"},
	{"WRK05"},
	{"STO01", "STO04", "STO11", "STO12", "TMP03"},
	{"BAK01", "BAK02", "BAK05", "MNT02"},
}

// LiveChecks are the red flags the minute refresh reads again; the page puts them in place of the
// overview's.
var LiveChecks = []string{"ACT01", "PRS01"}

// RedFlagsShown is how many flags the strip shows; the rest are counted.
const RedFlagsShown = 3

// RedFlag is one finding that is a red flag. To is where its button goes, relative to the database
// page: the investigation route the card already gives the finding ("blocking" for ACT01 and ACT02),
// else the card page ("cards/storage"). Rank orders the flags (lower first): Critical first, then the
// contract's table, then the card's order on the page and its own rank; the page orders the live flags
// in with it.
type RedFlag struct {
	CheckID  string   `json:"checkId"`
	Card     CardID   `json:"card"`
	Severity Severity `json:"severity"`
	Sentence string   `json:"sentence"`
	Object   string   `json:"object"`
	To       string   `json:"to"`
	Rank     int      `json:"rank"`
}

// RedFlags are the flags the strip shows (at most three), how many more there are, and those others in
// order (the "and {n} more" links to the first one's card; the page needs them when the live flags
// replace the overview's).
type RedFlags struct {
	Flags []RedFlag `json:"flags"`
	More  int       `json:"more"`
	Rest  []RedFlag `json:"rest"`
}

// redFlagRow is the row of the table a finding falls in, or -1 when it is not a red flag.
func redFlagRow(f Finding) int {
	for i, ids := range redFlagRows {
		for _, id := range ids {
			if id == f.CheckID && (f.Severity == SeverityHigh || f.Severity == SeverityCritical) {
				return i
			}
		}
	}
	if f.Severity == SeverityCritical {
		return len(redFlagRows)
	}
	return -1
}

// BuildRedFlags keeps the findings that are red flags, from every card's ranked findings, sorted Critical
// first, then in the table's order, then the cards' own rank, and returns the first three and the rest.
func BuildRedFlags(findings map[CardID][]Finding) RedFlags {
	return redFlagsOf(flagsOf(findings))
}

func flagsOf(findings map[CardID][]Finding) []RedFlag {
	out := []RedFlag{}
	for ci, def := range CardDefs {
		for i, f := range findings[def.ID] {
			row := redFlagRow(f)
			if row < 0 {
				continue
			}
			to := "cards/" + string(def.ID)
			if f.CheckID == "ACT01" || f.CheckID == "ACT02" {
				to = "blocking"
			}
			out = append(out, RedFlag{CheckID: f.CheckID, Card: def.ID, Severity: f.Severity, Sentence: f.Finding, Object: f.Object, To: to,
				Rank: f.Severity.Rank()*1_000_000 + row*10_000 + ci*1_000 + min(i, 999)})
		}
	}
	return out
}

func redFlagsOf(all []RedFlag) RedFlags {
	sort.SliceStable(all, func(i, j int) bool { return all[i].Rank < all[j].Rank })
	r := RedFlags{Flags: all[:min(len(all), RedFlagsShown)], Rest: []RedFlag{}}
	if len(all) > RedFlagsShown {
		r.Rest = all[RedFlagsShown:]
	}
	r.More = len(r.Rest)
	return r
}

// LiveRedFlags are the red flags among the live findings (LiveFindings: ACT01 and PRS01).
func LiveRedFlags(live []Finding) []RedFlag {
	return flagsOf(map[CardID][]Finding{CardPerformance: live})
}

// WithLive puts the live flags in place of the overview's ACT01 and PRS01 and keeps the rest.
func (r RedFlags) WithLive(live []RedFlag) RedFlags {
	all := []RedFlag{}
	for _, x := range append(append([]RedFlag{}, r.Flags...), r.Rest...) {
		if !isLiveCheck(x.CheckID) {
			all = append(all, x)
		}
	}
	return redFlagsOf(append(all, live...))
}

func isLiveCheck(id string) bool {
	for _, x := range LiveChecks {
		if x == id {
			return true
		}
	}
	return false
}

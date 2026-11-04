package domain

import (
	"strings"
	"testing"
)

var conditionColumns = []string{"Id", "Status", "LoggedAt", "Closed On", "Day", "Amount", "Note"}

// The allow list: twenty conditions the archive fix accepts, with the canonical form it writes.
func TestValidateConditionAllows(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"Status = 'Closed'", "[Status] = 'Closed'"},
		{"status = 'Closed'", "[Status] = 'Closed'"},
		{"[Status] <> N'Open'", "[Status] <> N'Open'"},
		{`"Status" != 'Open'`, "[Status] != 'Open'"},
		{"[Closed On] IS NOT NULL", "[Closed On] IS NOT NULL"},
		{"Note IS NULL", "[Note] IS NULL"},
		{"Amount BETWEEN 0 AND 10.5", "[Amount] BETWEEN 0 AND 10.5"},
		{"Status IN ('Closed', 'Cancelled')", "[Status] IN ('Closed', 'Cancelled')"},
		{"Status NOT IN ('Open')", "[Status] NOT IN ('Open')"},
		{"Note LIKE 'auto%'", "[Note] LIKE 'auto%'"},
		{"NOT (Status = 'Open' OR Status = 'Held')", "NOT ([Status] = 'Open' OR [Status] = 'Held')"},
		{"[Closed On] < DATEADD(DAY, -30, GETDATE())", "[Closed On] < DATEADD(DAY, - 30, GETDATE())"},
		{"DATEDIFF(month, LoggedAt, SYSDATETIME()) > 6", "DATEDIFF(MONTH, [LoggedAt], SYSDATETIME()) > 6"},
		{"CAST(LoggedAt AS date) < '2024-01-01'", "CAST([LoggedAt] AS DATE) < '2024-01-01'"},
		{"CONVERT(date, LoggedAt) <= '20240101'", "CONVERT(DATE, [LoggedAt]) <= '20240101'"},
		{"Day >= 3 and Amount < 100", "[Day] >= 3 AND [Amount] < 100"},
		{"Note = 'it''s done'", "[Note] = 'it''s done'"},
		{"((Amount * 2) + 1) > Id / 3", "(([Amount] * 2) + 1) > [Id] / 3"},
		{"  Status   =   'Closed'  ", "[Status] = 'Closed'"},
	} {
		got, err := CanonicalCondition(c.in, conditionColumns)
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.in, got, err, c.want)
		}
		if err := ValidateCondition(c.in, conditionColumns); err != nil {
			t.Errorf("%q: %v", c.in, err)
		}
	}
}

// The deny list: twenty conditions the archive fix refuses, each with a word of the reason.
func TestValidateConditionDenies(t *testing.T) {
	for _, c := range []struct{ in, why string }{
		{"Status = 'x'; DROP TABLE dbo.Log", "semicolon"},
		{"Status = 'x' -- the rest", "comment"},
		{"/* hi */ Status = 'x'", "comment"},
		{"Note = '*/ DROP TABLE x /*'", "comment"},
		{"Id IN (SELECT Id FROM Other)", "SELECT"},
		{"EXEC('DROP TABLE x')", "EXEC"},
		{"Id = @id", "variable"},
		{"xp_cmdshell('dir') = 0", "procedure"},
		{"sp_who = 1", "procedure"},
		{"Unknown = 1", "not a column"},
		{"[Nope] = 1", "not a column"},
		{"Status = 'x' UNION SELECT 1", "UNION"},
		{"EXISTS (Id)", "EXISTS"},
		{"Status = 'open", "not closed"},
		{"(Status = 'x'", "open"},
		{"Status = 'x')", "did not open"},
		{"Id = 0x01", "letters"},
		{"Status = 'x' WAITFOR DELAY '0:0:5'", "WAITFOR"},
		{"OPENROWSET('x') = 1", "OPENROWSET"},
		{"Status = `x`", "may not contain"},
	} {
		err := ValidateCondition(c.in, conditionColumns)
		if err == nil {
			t.Errorf("%q: accepted", c.in)
			continue
		}
		if !strings.Contains(err.Error(), c.why) {
			t.Errorf("%q: %v, want it to say %q", c.in, err, c.why)
		}
	}
	long := strings.Repeat("Id = 1 OR ", 120) + "Id = 1"
	if err := ValidateCondition(long, conditionColumns); err == nil {
		t.Errorf("a condition of %d characters was accepted", len(long))
	}
	// A column named like a refused word is written quoted.
	if got, err := CanonicalCondition("[Select] = 1", []string{"Select"}); err != nil || got != "[Select] = 1" {
		t.Errorf("quoted refused word: %q %v", got, err)
	}
	// A date part or a type is only a date part or a type where one belongs.
	if err := ValidateCondition("MONTH = 3", conditionColumns); err == nil {
		t.Error("MONTH outside DATEADD accepted")
	}
}

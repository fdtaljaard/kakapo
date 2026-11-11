package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSameMachine(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"SQL01", "sql01", true},
		{"SQL01", "sql01.corp.local", true},
		{"SQL01", "LAPTOP7", false},
		{"", "", false},
		{"SQL01", "", false},
	} {
		if got := SameMachine(c.a, c.b); got != c.want {
			t.Errorf("SameMachine(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// A step done on the SQL Server machine says so in the JSON; any other leaves the field out.
func TestFindingOnServerMachine(t *testing.T) {
	b, err := json.Marshal(Finding{CheckID: "INS07", Title: "Instant file initialization"})
	if err != nil || !strings.Contains(string(b), `"onServerMachine":true`) || !strings.Contains(string(b), `"checkId":"INS07"`) {
		t.Errorf("INS07: %s %v", b, err)
	}
	b, _ = json.Marshal(Finding{CheckID: "DBC08"})
	if strings.Contains(string(b), "onServerMachine") {
		t.Errorf("DBC08: %s", b)
	}
	var back Finding
	if err := json.Unmarshal([]byte(`{"checkId":"INS13","onServerMachine":true}`), &back); err != nil || back.CheckID != "INS13" {
		t.Errorf("read back: %+v %v", back, err)
	}
}

package collect

import (
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

func TestConnString(t *testing.T) {
	cases := []struct {
		name string
		in   domain.ServerInput
		want []string
		not  []string
	}{
		{
			name: "named instance, windows auth",
			in:   domain.ServerInput{Host: `CLIENTSQL01\WMS`, Auth: domain.AuthWindows},
			want: []string{"odbc:server={CLIENTSQL01\\WMS}", "encrypt=false", "app name=SQLMonitor"},
			not:  []string{"user id", "password", "port="},
		},
		{
			name: "host and port, sql login with awkward password",
			in:   domain.ServerInput{Host: "172.20.1.100,1433", Auth: domain.AuthSQL, Username: "sa", Password: "p;a}s", Database: "WarehouseLive", Encrypt: true, TrustCert: true},
			want: []string{"server={172.20.1.100}", "port=1433", "user id={sa}", "password={p;a}}s}", "database={WarehouseLive}", "encrypt=true", "TrustServerCertificate=true"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ConnString(c.in)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("want %q in %q", w, got)
				}
			}
			for _, n := range c.not {
				if strings.Contains(got, n) {
					t.Errorf("do not want %q in %q", n, got)
				}
			}
		})
	}
}

// untagged is a read batch without the tags TagStatements puts after each statement's keyword, as the
// tests that read the SQL text expect it.
func untagged(q string) string { return strings.ReplaceAll(q, " "+KakapoTag, "") }

// Phase 5k: every statement of a read batch carries the tag after its first keyword, once, so Query
// Store's text of each statement has it; a keyword inside a line is left alone.
func TestTagStatements(t *testing.T) {
	in := "SET NOCOUNT ON;\nSELECT a FROM b;\n    INSERT #t (x)\n    SELECT TOP (1) x FROM y WHERE z = 'SELECT';\nWITH c AS (SELECT 1 AS n)\nUPDATE t SET n = 1;\nDELETE #t;\nselect lower;"
	want := "/* kakapo */ SET NOCOUNT ON;\nSELECT /* kakapo */ a FROM b;\n    INSERT /* kakapo */ #t (x)\n    SELECT /* kakapo */ TOP (1) x FROM y WHERE z = 'SELECT';\nWITH /* kakapo */ c AS (SELECT 1 AS n)\nUPDATE /* kakapo */ t SET n = 1;\nDELETE /* kakapo */ #t;\nselect lower;"
	if got := TagStatements(in); got != want {
		t.Errorf("tagged:\n%s", got)
	}
	if got := TagStatements(TagStatements(in)); got != want {
		t.Errorf("twice:\n%s", got)
	}
}

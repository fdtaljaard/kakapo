package collect

import (
	"testing"
	"time"

	"github.com/microsoft/go-mssqldb/msdsn"

	"sqlmonitor/internal/domain"
)

// The driver must see the instance name, not the braces, when we quote the server.
func TestConnStringParsesInstance(t *testing.T) {
	in := domain.ServerInput{Host: `localhost\SQL2022`, Auth: domain.AuthSQL, Username: "sa", Password: "p;a}s"}
	cfg, err := msdsn.Parse(ConnString(in))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "localhost" || cfg.Instance != "SQL2022" {
		t.Errorf("host=%q instance=%q from %q", cfg.Host, cfg.Instance, ConnString(in))
	}
	if cfg.User != "sa" || cfg.Password != "p;a}s" {
		t.Errorf("user=%q password=%q", cfg.User, cfg.Password)
	}
	// Phase 4f: the TCP keepalive is set explicitly, so a long fix's idle connection stays open.
	if cfg.KeepAlive != 30*time.Second {
		t.Errorf("keepAlive=%v from %q", cfg.KeepAlive, ConnString(in))
	}
}

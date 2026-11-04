package service

import (
	"context"
	"time"

	"sqlmonitor/internal/domain"
)

// The fix registry (docs/phase4c-contract.md, section 0): a fix that is not a plain setting
// registers how it is prepared (facts read, preview built into g.preview and g.fix) and how it is
// applied, from its own file's init(). prepareGenericFix and ApplyFixWithOptions consult the
// registry before their built-in branches, so a new fix never edits them.

type fixPreparer func(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error

type fixApplier func(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error)

type registeredFix struct {
	prepare fixPreparer
	apply   fixApplier
	// long is set for a fix that can run for minutes or hours (docs/phase4f-contract.md):
	// ApplyFixWithOptions starts it in the background and answers at once with a pending run.
	long *longFix
}

// longFix is what a long fix registers besides prepare and apply: the outer bound of its run and
// the check a run uses when the apply has not returned and its request has gone from the server
// (the connection was lost).
type longFix struct {
	timeout   time.Duration
	completed completionCheck
}

var fixRegistry = map[string]registeredFix{}

// registerFix adds a fix to the registry. Both functions are required. Registering an id twice
// is a programming error.
func registerFix(id string, prepare fixPreparer, apply fixApplier) {
	addFix(id, registeredFix{prepare: prepare, apply: apply})
}

// registerLongFix adds a long fix to the registry (docs/phase4f-contract.md): its apply runs in
// the background, bounded by long.timeout, and long.completed reads the outcome back when the
// connection is lost during the run. Both are required.
func registerLongFix(id string, prepare fixPreparer, apply fixApplier, long longFix) {
	if long.timeout <= 0 || long.completed == nil {
		panic("registerLongFix " + id + ": a timeout and a completion check are both needed")
	}
	addFix(id, registeredFix{prepare: prepare, apply: apply, long: &long})
}

func addFix(id string, r registeredFix) {
	if r.prepare == nil || r.apply == nil {
		panic("registerFix " + id + ": prepare and apply are both needed")
	}
	if _, dup := fixRegistry[id]; dup {
		panic("registerFix " + id + ": registered twice")
	}
	fixRegistry[id] = r
}

// registeredFixFor returns the registered fix for an id, if any.
func registeredFixFor(id string) (registeredFix, bool) {
	r, ok := fixRegistry[id]
	return r, ok
}

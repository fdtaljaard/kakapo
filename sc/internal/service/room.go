package service

import (
	"context"
	"database/sql"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// resolveRoom turns what a preview's builder says the change will use (p.Needs) into the room line
// (docs/phase5l-contract.md): the server's volumes, read now, and the stored trend of each. A
// volume read that fails leaves the lines Not readable; it never fails the preview.
func (s *Service) resolveRoom(ctx context.Context, serverID int64, db *sql.DB, database string, p *domain.FixPreview) {
	if len(p.Needs) == 0 {
		return
	}
	needs := p.Needs
	if domain.NeedsRoles(needs) {
		paths, err := collect.RolePaths(ctx, db)
		if err != nil {
			paths = nil
		}
		needs = domain.ResolveRoles(needs, database, paths)
	}
	vols, err := collect.Volumes(ctx, db)
	if err != nil {
		vols = nil
	}
	trends := map[string]domain.DiskTrend{}
	for _, v := range vols {
		if s.store == nil {
			break
		}
		if t, err := s.store.DiskTrend(ctx, serverID, v.Name); err == nil && t != nil {
			trends[v.Name] = *t
		}
	}
	p.Room = domain.BuildRoom(needs, domain.RoomFacts{Volumes: vols, Trends: trends})
}

// roomHeld is Apply's answer when the change does not fit on one of its drives: nothing runs.
func roomHeld(p domain.FixPreview) error {
	if domain.RoomHeld(p.Room) {
		return newError(KindConflict, "%s", domain.RoomHeldReason(p.Room))
	}
	return nil
}

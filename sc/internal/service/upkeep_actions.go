package service

import (
	"context"
	"strconv"
	"strings"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The Upkeep card's actions (docs/phase4d-contract.md, part 1), registered with the fix registry:
// operator-alerts, maintenance-job and remove-shrink-step. Each prepare reads msdb
// (collect/upkeep_actions.go) and builds the preview in domain; each apply runs the previewed
// batch through collect.ExecFix, reads msdb back and writes the change row with the rollback.

func init() {
	registerFix(domain.FixOperatorAlerts, prepareOperatorAlerts, applyOperatorAlerts)
	registerFix(domain.FixMaintenanceJob, prepareMaintenanceJob, applyMaintenanceJob)
	registerFix(domain.FixRemoveShrinkStep, prepareRemoveShrinkStep, applyRemoveShrinkStep)
}

// --- operator-alerts ---

func prepareOperatorAlerts(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadAlertFacts(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "%s: %v", g.srv.Name, err)
	}
	return operatorAlertsPreview(g, f, options)
}

func operatorAlertsPreview(g *genericFix, f domain.AlertFacts, options map[string]string) error {
	if f.EngineEdition == 4 {
		return newError(KindConflict, "SQL Server Express has no Agent, so it has no operators or alerts")
	}
	p, fix, err := domain.BuildOperatorAlertsFix(f, options)
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.OperatorAlertsDescription(suServerLabel(g, f.ServerName), p.Changes))
	return suPreviewed(g, p, fix)
}

func applyOperatorAlerts(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to create the operator and alerts on %s: %v. %s",
			g.srv.Name, err, domain.AlertsPermission(g.srv.Name))
	}
	change := suChange(serverID, g, "Alerts", "ALERT", g.options[suDescriptionKey])
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadAlertFacts(rctx, g.db)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The operator and alerts were created; they")
	}
	alerts := strings.EqualFold(g.preview.Options[domain.OptionCreateAlerts], "true")
	after, ok := domain.OperatorAlertsReadBack(g.preview.Options[domain.OptionOperatorName], alerts, f)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

// --- maintenance-job ---

func prepareMaintenanceJob(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadMaintenanceJobFacts(rctx, g.db, g.database.Name)
	if err != nil {
		return newError(KindUnreachable, "%s: %v", g.srv.Name, err)
	}
	return maintenanceJobPreview(g, f, options)
}

func maintenanceJobPreview(g *genericFix, f domain.MaintenanceJobFacts, options map[string]string) error {
	p, fix, err := domain.BuildMaintenanceJobFix(f, options)
	if err != nil {
		return err
	}
	suKeep(g, suJobsKey, strings.Join(domain.MaintenanceJobNames(f, options), "\n"))
	suKeep(g, suDescriptionKey, domain.MaintenanceJobDescription(fix))
	return suPreviewed(g, p, fix)
}

func applyMaintenanceJob(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to create the maintenance job on %s: %v. %s",
			g.srv.Name, err, domain.BackupFixPermission(g.srv.Name))
	}
	change := suChange(serverID, g, "Maintenance job", "JOB", g.options[suDescriptionKey])
	names := strings.Split(g.options[suJobsKey], "\n")
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	states, err := collect.ReadJobsByName(rctx, g.db, names)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The maintenance job was created; it")
	}
	after, ok := domain.KakapoJobsReadBack(names, states)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

// --- remove-shrink-step ---

func prepareRemoveShrinkStep(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	job := strings.TrimSpace(options[domain.OptionJobName])
	if job == "" {
		return newError(KindInvalid, "the shrink step to remove needs its job (job) and step number (step)")
	}
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadJobSteps(rctx, g.db, job)
	if err != nil {
		return newError(KindUnreachable, "%s: %v", g.srv.Name, err)
	}
	return shrinkStepPreview(g, f, options)
}

func shrinkStepPreview(g *genericFix, f domain.ShrinkStepFacts, options map[string]string) error {
	if !f.Found {
		return newError(KindNotFound, "there is no Agent job named %s on %s", f.Job, g.srv.Name)
	}
	p, fix, err := domain.BuildRemoveShrinkStepFix(f, options)
	if err != nil {
		return err
	}
	if n, err := strconv.Atoi(p.Options[domain.OptionJobStep]); err == nil {
		for _, st := range f.Steps {
			if st.ID == n {
				suKeep(g, suStepNameKey, st.Name)
				suKeep(g, suStepCommandKey, st.Command)
			}
		}
	}
	suKeep(g, suDescriptionKey, domain.RemoveShrinkStepDescription(fix, suServerLabel(g, f.ServerName)))
	return suPreviewed(g, p, fix)
}

func applyRemoveShrinkStep(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	job := g.preview.Options[domain.OptionJobName]
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to change the job %s: %v. %s", job, err, domain.ShrinkStepPermission(g.srv.Name))
	}
	change := suChange(serverID, g, g.fix.Setting, "JOB", g.options[suDescriptionKey])
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadJobSteps(rctx, g.db, job)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The change to the job "+job)
	}
	removed := domain.AgentJobStep{Name: g.options[suStepNameKey], Command: g.options[suStepCommandKey]}
	after, ok := domain.RemoveShrinkStepReadBack(g.fix.Target == "disabled", removed, f)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

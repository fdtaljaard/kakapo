package domain

import (
	"fmt"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The Upkeep card's actions (docs/phase4d-contract.md, part 1), each built through the fix registry
// from what was read just before: operator-alerts creates the operator and the alerts MNT07 misses
// and makes the silent ones mail it, maintenance-job creates the nightly statistics job (and on
// request the weekly index job) MNT03 misses, and remove-shrink-step takes a shrink step out of an
// Agent job (MNT05). A preview that cannot be applied has warnings and no changes.

// Ids of the Upkeep card's actions.
const (
	FixOperatorAlerts   = "operator-alerts"
	FixMaintenanceJob   = "maintenance-job"
	FixRemoveShrinkStep = "remove-shrink-step"
)

// The options of the Upkeep card's actions.
const (
	OptionOperatorName  = "operator"       // operator-alerts: the operator's name
	OptionOperatorEmail = "email"          // operator-alerts: its email address, when it is created
	OptionCreateAlerts  = "alerts"         // operator-alerts: create the missing alerts and notifications
	OptionMaintTime     = "time"           // maintenance-job: HH:MM, server time
	OptionStatsOnly     = "statisticsOnly" // maintenance-job: no weekly index job
	OptionJobName       = "job"            // remove-shrink-step: the job
	OptionJobStep       = "step"           // remove-shrink-step: the step number
)

// DefaultOperatorName is the operator operator-alerts creates when none has an email address.
const DefaultOperatorName = "Database support"

// DefaultMaintenanceTime is when the maintenance jobs run by default.
const DefaultMaintenanceTime = "01:00"

// --- operator-alerts (MNT07) ---

// AgentOperator is one row of msdb.dbo.sysoperators.
type AgentOperator struct {
	Name    string
	Email   string
	Enabled bool
}

// AgentAlert is one row of msdb.dbo.sysalerts.
type AgentAlert struct {
	Name      string
	Severity  int
	MessageID int
	Enabled   bool
	Database  string // "" for every database
	Keyword   string // event_description_keyword; "" none
}

// AgentNotification is one row of msdb.dbo.sysnotifications, by name.
type AgentNotification struct {
	Alert    string
	Operator string
	Method   int // bits: 1 email, 2 pager, 4 net send
}

// AlertFacts is what operator-alerts reads from msdb and about Database Mail and the Agent.
type AlertFacts struct {
	ServerName       string
	ProductVersion   string
	EngineEdition    int
	Operators        []AgentOperator
	Alerts           []AgentAlert
	Notifications    []AgentNotification
	MailProfiles     int    // rows in msdb.dbo.sysmail_profile
	MailError        string // Database Mail could not be read
	AgentMailProfile string // the profile the Agent's alert system mails with; "" none
	AgentMailError   string
	AgentStatus      string // Running, Stopped; "" not visible
	AgentError       string
}

// alertSpec is one alert of the scan's list (MNT07): severity 19 to 25 and errors 823, 824, 825.
type alertSpec struct {
	Severity int
	Message  int
	Label    string // "severity 19", "error 823"
}

// Name is the name kakapo gives the alert it creates.
func (a alertSpec) Name() string { return "kakapo: " + a.Label }

func (a alertSpec) covers(x AgentAlert) bool {
	return (a.Severity > 0 && x.Severity == a.Severity) || (a.Message > 0 && x.MessageID == a.Message)
}

// kakapoAlerts is the scan's alert list, in its order.
var kakapoAlerts = func() []alertSpec {
	out := []alertSpec{}
	for s := 19; s <= 25; s++ {
		out = append(out, alertSpec{Severity: s, Label: fmt.Sprintf("severity %d", s)})
	}
	for _, m := range []int{823, 824, 825} {
		out = append(out, alertSpec{Message: m, Label: fmt.Sprintf("error %d", m)})
	}
	return out
}()

func (f AlertFacts) operator(name string) (AgentOperator, bool) {
	for _, o := range f.Operators {
		if strings.EqualFold(o.Name, name) {
			return o, true
		}
	}
	return AgentOperator{}, false
}

// mailOperators are the enabled operators with an email address, by name.
func (f AlertFacts) mailOperators() []AgentOperator {
	out := []AgentOperator{}
	for _, o := range f.Operators {
		if o.Enabled && strings.TrimSpace(o.Email) != "" {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// missingAlerts are the scan's alerts no enabled alert covers, as MNT07 reads them.
func (f AlertFacts) missingAlerts() []alertSpec {
	out := []alertSpec{}
	for _, s := range kakapoAlerts {
		found := false
		for _, a := range f.Alerts {
			found = found || (a.Enabled && s.covers(a))
		}
		if !found {
			out = append(out, s)
		}
	}
	return out
}

func isWatchedAlert(a AgentAlert) bool {
	return (a.Severity >= 19 && a.Severity <= 25) || a.MessageID == 823 || a.MessageID == 824 || a.MessageID == 825
}

// mails says whether an alert mails an enabled operator with an email address.
func (f AlertFacts) mails(alert string) bool {
	for _, n := range f.Notifications {
		if !strings.EqualFold(n.Alert, alert) || n.Method&1 == 0 {
			continue
		}
		if o, ok := f.operator(n.Operator); ok && o.Enabled && strings.TrimSpace(o.Email) != "" {
			return true
		}
	}
	return false
}

// SilentAlerts are the enabled watched alerts that mail no one, by name (MNT07's SilentAlerts).
func (f AlertFacts) SilentAlerts() []string {
	out := []string{}
	for _, a := range f.Alerts {
		if a.Enabled && isWatchedAlert(a) && !f.mails(a.Name) {
			out = append(out, a.Name)
		}
	}
	sort.Strings(out)
	return out
}

func (f AlertFacts) notification(alert, operator string) (AgentNotification, bool) {
	for _, n := range f.Notifications {
		if strings.EqualFold(n.Alert, alert) && strings.EqualFold(n.Operator, operator) {
			return n, true
		}
	}
	return AgentNotification{}, false
}

func (f AlertFacts) alertNamed(name string) (AgentAlert, bool) {
	for _, a := range f.Alerts {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return AgentAlert{}, false
}

// ValidEmail says whether an operator email address is one address kakapo can put in a statement:
// name@domain.tld, at most 100 characters (sysoperators.email_address).
func ValidEmail(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 100 || strings.ContainsAny(s, " ;,'\"<>") {
		return false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s {
		return false
	}
	_, domain, ok := strings.Cut(s, "@")
	return ok && strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

// validObjectName says whether a name typed for an operator can be one: 1 to 128 characters, no
// control characters.
func validObjectName(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 128 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// alertPlan is what operator-alerts does, from the facts and the options.
type alertPlan struct {
	operator       string
	email          string
	createOperator bool
	alerts         bool
	create         []alertSpec         // alerts kakapo creates
	enable         []AgentAlert        // disabled alerts on the condition, enabled again
	notify         []string            // existing alerts given an email notification to the operator
	widen          []AgentNotification // existing notifications to the operator given email too
	blocked        []string            // why nothing can be applied
	inputs         []FixInput
	options        map[string]string
}

const (
	operatorHelp   = "The operator the alerts mail: usually the client's IT mailbox or the support desk."
	emailHelp      = "The address that receives the alerts, for example dba@client.co.za."
	alertsHelp     = "Create one alert per severity 19 to 25 and per error 823, 824 and 825, and make every one of them mail the operator."
	operatorPick   = "The operator with an email address the alerts mail."
	emailMissing   = "Type the email address that receives the alerts, for example dba@client.co.za."
	alertsNoChange = "Nothing to create: the operator exists and the alerts are not asked for."
)

func planAlerts(f AlertFacts, options map[string]string) alertPlan {
	p := alertPlan{alerts: boolOption(options, OptionCreateAlerts), options: map[string]string{}}
	mailOps := f.mailOperators()
	typedName, nameGiven := options[OptionOperatorName]
	typedName = strings.TrimSpace(typedName)
	if len(mailOps) > 0 {
		names := make([]string, 0, len(mailOps))
		for _, o := range mailOps {
			names = append(names, o.Name)
		}
		p.operator, p.email = mailOps[0].Name, mailOps[0].Email
		in := FixInput{Name: OptionOperatorName, Label: "Operator", Kind: "select", Options: names, Help: operatorPick}
		if nameGiven && typedName != "" {
			if o, ok := f.operator(typedName); ok && o.Enabled && strings.TrimSpace(o.Email) != "" {
				p.operator, p.email = o.Name, o.Email
			} else {
				in.Warning = "Choose one of the operators with an email address: " + strings.Join(names, ", ") + "."
				p.operator = typedName
				p.blocked = append(p.blocked, in.Warning)
			}
		}
		in.Value = p.operator
		p.inputs = append(p.inputs, in)
		p.options[OptionOperatorName] = p.operator
	} else {
		p.createOperator = true
		p.operator = DefaultOperatorName
		if nameGiven && typedName != "" {
			p.operator = typedName
		}
		name := FixInput{Name: OptionOperatorName, Label: "Operator name", Value: p.operator, Kind: "text", Help: operatorHelp}
		switch {
		case !validObjectName(p.operator):
			name.Warning = "The operator name must be 1 to 128 characters."
		default:
			if o, ok := f.operator(p.operator); ok {
				state := "without an email address"
				if !o.Enabled {
					state = "but disabled"
				}
				name.Warning = "An operator named " + o.Name + " already exists " + state + ": type another name, or fix that operator in SSMS."
			}
		}
		p.email = strings.TrimSpace(options[OptionOperatorEmail])
		email := FixInput{Name: OptionOperatorEmail, Label: "Email address", Value: p.email, Kind: "text", Help: emailHelp}
		switch {
		case p.email == "":
			email.Warning = emailMissing
		case !ValidEmail(p.email):
			email.Warning = p.email + " is not an email address: type one address, for example dba@client.co.za."
		}
		for _, w := range []string{name.Warning, email.Warning} {
			if w != "" {
				p.blocked = append(p.blocked, w)
			}
		}
		p.inputs = append(p.inputs, name, email)
		p.options[OptionOperatorName], p.options[OptionOperatorEmail] = p.operator, p.email
	}
	p.inputs = append(p.inputs, FixInput{Name: OptionCreateAlerts, Label: "Create the missing alerts", Value: fmt.Sprint(p.alerts), Kind: "bool", Help: alertsHelp})
	p.options[OptionCreateAlerts] = fmt.Sprint(p.alerts)
	if !p.alerts {
		return p
	}

	toNotify := []string{}
	for _, s := range f.missingAlerts() {
		var dormant *AgentAlert
		for i := range f.Alerts {
			a := f.Alerts[i]
			if !a.Enabled && s.covers(a) && a.Database == "" && a.Keyword == "" {
				dormant = &a
				break
			}
		}
		switch {
		case dormant != nil:
			p.enable = append(p.enable, *dormant)
			toNotify = append(toNotify, dormant.Name)
		default:
			if a, taken := f.alertNamed(s.Name()); taken {
				p.blocked = append(p.blocked, "An alert named "+a.Name+" already exists on another condition: rename or delete it in SSMS first.")
				continue
			}
			p.create = append(p.create, s)
		}
	}
	for _, a := range f.Alerts {
		if a.Enabled && isWatchedAlert(a) && !f.mails(a.Name) {
			toNotify = append(toNotify, a.Name)
		}
	}
	for _, name := range toNotify {
		if n, ok := f.notification(name, p.operator); ok {
			if n.Method&1 == 0 {
				p.widen = append(p.widen, n)
			}
			continue
		}
		p.notify = append(p.notify, name)
	}
	return p
}

func (p alertPlan) empty() bool {
	return !p.createOperator && len(p.create) == 0 && len(p.enable) == 0 && len(p.notify) == 0 && len(p.widen) == 0
}

// batch is the statement: everything in one transaction inside TRY ... CATCH.
// Source: DarlingData Alerts/Alerts.sql 56-68 (MIT): the sp_add_alert parameters (delay 60 seconds,
// the event description in the email) and the sp_add_notification per alert.
func (p alertPlan) batch() string {
	var b strings.Builder
	op := nstr(p.operator)
	b.WriteString("BEGIN TRY\n    BEGIN TRANSACTION;\n")
	if p.createOperator {
		b.WriteString("    EXEC msdb.dbo.sp_add_operator @name = " + op + ", @enabled = 1, @email_address = " + nstr(p.email) + ";\n")
	}
	for _, s := range p.create {
		fmt.Fprintf(&b, "    EXEC msdb.dbo.sp_add_alert @name = %s, @message_id = %d, @severity = %d, @enabled = 1,\n", nstr(s.Name()), s.Message, s.Severity)
		b.WriteString("        @delay_between_responses = 60, @include_event_description_in = 1;\n")
		b.WriteString("    EXEC msdb.dbo.sp_add_notification @alert_name = " + nstr(s.Name()) + ", @operator_name = " + op + ", @notification_method = 1;\n")
	}
	for _, a := range p.enable {
		b.WriteString("    EXEC msdb.dbo.sp_update_alert @name = " + nstr(a.Name) + ", @enabled = 1;\n")
	}
	for _, name := range p.notify {
		b.WriteString("    EXEC msdb.dbo.sp_add_notification @alert_name = " + nstr(name) + ", @operator_name = " + op + ", @notification_method = 1;\n")
	}
	for _, n := range p.widen {
		fmt.Fprintf(&b, "    EXEC msdb.dbo.sp_update_notification @alert_name = %s, @operator_name = %s, @notification_method = %d;\n", nstr(n.Alert), op, n.Method|1)
	}
	b.WriteString("    COMMIT TRANSACTION;\nEND TRY\nBEGIN CATCH\n    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;\n    THROW;\nEND CATCH;")
	return b.String()
}

// rollback deletes what the batch created and puts back what it changed, by the names recorded.
func (p alertPlan) rollback() string {
	lines := []string{}
	op := nstr(p.operator)
	for _, n := range p.widen {
		lines = append(lines, fmt.Sprintf("EXEC msdb.dbo.sp_update_notification @alert_name = %s, @operator_name = %s, @notification_method = %d;", nstr(n.Alert), op, n.Method))
	}
	if !p.createOperator {
		for _, name := range p.notify {
			lines = append(lines, "EXEC msdb.dbo.sp_delete_notification @alert_name = "+nstr(name)+", @operator_name = "+op+";")
		}
	}
	for _, a := range p.enable {
		lines = append(lines, "EXEC msdb.dbo.sp_update_alert @name = "+nstr(a.Name)+", @enabled = 0;")
	}
	for _, s := range p.create {
		lines = append(lines, "EXEC msdb.dbo.sp_delete_alert @name = "+nstr(s.Name())+";")
	}
	if p.createOperator {
		// Deleting the operator deletes its notifications too.
		lines = append(lines, "EXEC msdb.dbo.sp_delete_operator @name = "+op+";")
	}
	return strings.Join(lines, "\n")
}

func (p alertPlan) changes() []Drift {
	out := []Drift{}
	if p.createOperator {
		out = append(out, Drift{Setting: "Operator", Current: "none with an email address", Target: p.operator + " (" + p.email + ")"})
	} else {
		out = append(out, Drift{Setting: "Operator", Current: p.operator + " (" + p.email + ")", Target: "mailed by the alerts"})
	}
	for _, s := range p.create {
		out = append(out, Drift{Setting: "Alert " + s.Label, Current: "none", Target: s.Name() + ", mails " + p.operator})
	}
	for _, a := range p.enable {
		out = append(out, Drift{Setting: "Alert " + a.Name, Current: "disabled", Target: "enabled, mails " + p.operator})
	}
	enabled := map[string]bool{}
	for _, a := range p.enable {
		enabled[strings.ToLower(a.Name)] = true
	}
	for _, name := range p.notify {
		if !enabled[strings.ToLower(name)] {
			out = append(out, Drift{Setting: "Alert " + name, Current: "mails no one", Target: "mails " + p.operator})
		}
	}
	for _, n := range p.widen {
		if !enabled[strings.ToLower(n.Alert)] {
			out = append(out, Drift{Setting: "Alert " + n.Alert, Current: "notifies " + p.operator + " without email", Target: "mails " + p.operator})
		}
	}
	return out
}

// mailWarning is the Database Mail warning: "" when mail can leave the server.
func (f AlertFacts) mailWarning() string {
	switch {
	case f.MailError != "":
		return "kakapo could not check Database Mail (" + f.MailError + "): make sure it is set up, or the alerts mail no one."
	case f.MailProfiles == 0:
		return "Alerts are created but no mail leaves this server until Database Mail is set up: copy this for IT."
	case f.AgentMailError == "" && strings.TrimSpace(f.AgentMailProfile) == "":
		return "Alerts are created but no mail leaves this server until SQL Server Agent uses Database Mail (SQL Server Agent > Properties > Alert System: enable the mail profile, then restart the Agent): copy this for IT."
	}
	return ""
}

// AlertsPermission is what operator-alerts needs and names when SQL Server refuses.
func AlertsPermission(server string) string {
	return "Needs sysadmin on " + server + " (creating operators and alerts in msdb)."
}

// BuildOperatorAlertsFix builds the operator-alerts preview with the options (operator, email,
// alerts): the operator created when none has an email address, each missing alert created (or a
// disabled one on the condition enabled again) with an email notification, and the silent alerts
// given one. ErrFixNotApplicable when an operator with an email exists, no alert is missing and
// none is silent. A missing or wrong email address, or a name already taken, leaves the preview
// without changes.
func BuildOperatorAlertsFix(f AlertFacts, options map[string]string) (FixPreview, Fix, error) {
	if len(f.mailOperators()) > 0 && len(f.missingAlerts()) == 0 && len(f.SilentAlerts()) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	plan := planAlerts(f, options)
	fix := suFix(FixOperatorAlerts)
	fix.Current = alertsCurrent(len(f.mailOperators()), len(f.missingAlerts()), len(f.SilentAlerts()))
	fix.Target = "operator " + plan.operator + ", every alert mails it"
	fix.Options = plan.options
	p := FixPreview{EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Inputs: plan.inputs, Options: plan.options}
	p.Warnings = append(p.Warnings, plan.blocked...)
	if len(plan.blocked) == 0 && plan.empty() {
		p.Warnings = append(p.Warnings, alertsNoChange)
	}
	if w := f.mailWarning(); w != "" {
		p.Warnings = append(p.Warnings, w)
	}
	if f.AgentError == "" && f.AgentStatus != "" && f.AgentStatus != "Running" {
		p.Warnings = append(p.Warnings, "SQL Server Agent is "+strings.ToLower(f.AgentStatus)+": alerts are raised only while it runs (MNT01).")
	}
	p.Warnings = append(p.Warnings,
		"To test it, from a throwaway SSMS window: RAISERROR('Alert test', 20, 1) WITH LOG (it closes that window); the operator gets a mail within a minute.",
		AlertsPermission(f.ServerName))
	if len(plan.blocked) > 0 || plan.empty() {
		return p, fix, nil
	}
	p.Changes = plan.changes()
	p.Statement, p.Rollback = plan.batch(), plan.rollback()
	return p, fix, nil
}

func alertsCurrent(operators, missing, silent int) string {
	parts := []string{fmt.Sprintf("%d operator(s) with email", operators)}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d alert(s) missing", missing))
	}
	if silent > 0 {
		parts = append(parts, fmt.Sprintf("%d alert(s) mail no one", silent))
	}
	return strings.Join(parts, ", ")
}

// OperatorAlertsReadBack says what msdb holds after the statement: the operator with its email,
// the alerts still missing and those that still mail no one, and whether all is as asked.
func OperatorAlertsReadBack(operator string, alerts bool, after AlertFacts) (string, bool) {
	parts, ok := []string{}, true
	if o, found := after.operator(operator); found && o.Enabled && strings.TrimSpace(o.Email) != "" {
		parts = append(parts, "operator "+o.Name+" ("+o.Email+")")
	} else {
		parts, ok = append(parts, "operator "+operator+" not found with an email address"), false
	}
	if alerts {
		missing := []string{}
		for _, s := range after.missingAlerts() {
			missing = append(missing, s.Label)
		}
		if len(missing) > 0 {
			parts, ok = append(parts, "still missing: "+strings.Join(missing, ", ")), false
		} else {
			parts = append(parts, "no alert missing")
		}
		if silent := after.SilentAlerts(); len(silent) > 0 {
			parts, ok = append(parts, "still mailing no one: "+strings.Join(silent, ", ")), false
		} else {
			parts = append(parts, "every alert mails someone")
		}
	}
	return strings.Join(parts, "; "), ok
}

// OperatorAlertsDescription is the change log line of operator-alerts, from the preview's changes.
func OperatorAlertsDescription(server string, changes []Drift) string {
	created, enabled, notified := 0, 0, 0
	operator := ""
	for _, c := range changes {
		switch {
		case c.Setting == "Operator" && c.Current == "none with an email address":
			operator = "Created the operator " + c.Target
		case c.Setting == "Operator":
			operator = "Used the operator " + c.Current
		case c.Current == "none":
			created++
		case c.Current == "disabled":
			enabled++
		default:
			notified++
		}
	}
	return fmt.Sprintf("%s on %s; %d alert(s) created, %d enabled again, %d more now mail it", operator, server, created, enabled, notified)
}

// --- maintenance-job (MNT03) ---

// MaintenanceJobFacts is what maintenance-job reads: whether Ola Hallengren's IndexOptimize is in
// master, which of kakapo's maintenance jobs exist, the operator, the Agent and the server's clock.
type MaintenanceJobFacts struct {
	ServerName     string
	ProductVersion string
	EngineEdition  int // 4 is Express: no Agent
	Database       string
	Ola            bool     // master.dbo.IndexOptimize and master.dbo.CommandExecute exist
	ExistingJobs   []string // of StatisticsJobName and IndexJobName
	Operator       string   // the first enabled operator with an email address; "" none
	AgentStatus    string
	AgentError     string
	ServerNow      time.Time
}

// StatisticsJobName is the nightly statistics job kakapo creates for a database.
func StatisticsJobName(db string) string { return "kakapo: statistics (" + db + ")" }

// IndexJobName is the weekly index job kakapo creates for a database.
func IndexJobName(db string) string { return "kakapo: indexes (" + db + ")" }

// OlaDatabaseNameOK says whether a database name can go in IndexOptimize's @Databases as it is: Ola
// reads commas as lists, % as a wildcard and a leading - as an exclusion.
func OlaDatabaseNameOK(db string) bool {
	return db != "" && !strings.ContainsAny(db, ",%[]") && !strings.HasPrefix(db, "-")
}

// Source: DarlingData Ola Stats Only Job/Ola Stats Only Job.sql 107 (MIT): IndexOptimize with no
// index work and modified statistics only (@StatisticsModificationLevel = 5), scoped to the
// database.
func olaStatisticsCommand(db string) string {
	return "EXECUTE master.dbo.IndexOptimize @Databases = " + nstr(db) + ", @FragmentationLow = NULL, @FragmentationMedium = NULL, @FragmentationHigh = NULL, @UpdateStatistics = 'ALL', @OnlyModifiedStatistics = 'Y', @StatisticsModificationLevel = 5;"
}

func olaIndexCommand(db string) string {
	return "EXECUTE master.dbo.IndexOptimize @Databases = " + nstr(db) + ", @FragmentationLow = NULL, @FragmentationMedium = 'INDEX_REORGANIZE', @FragmentationHigh = 'INDEX_REBUILD_ONLINE,INDEX_REBUILD_OFFLINE', @FragmentationLevel1 = 30, @FragmentationLevel2 = 50, @MinNumberOfPages = 1000, @UpdateStatistics = 'ALL', @OnlyModifiedStatistics = 'Y', @StatisticsModificationLevel = 5;"
}

func updateStatsCommand(db string) string { return "EXEC " + QuoteName(db) + ".sys.sp_updatestats;" }

func (f MaintenanceJobFacts) exists(name string) bool {
	for _, n := range f.ExistingJobs {
		if n == name {
			return true
		}
	}
	return false
}

const (
	maintTimeHelp   = "Daily, server time. Pick a quiet hour, away from the backups (the full backup job runs at its own time)."
	maintTimeWarn   = "The time must be HH:MM, for example 01:00."
	statsOnlyHelp   = "Statistics matter most. Untick to add a weekly index job (Sunday at the same time): reorganise from 30% fragmentation, rebuild from 50%, tables over 1,000 pages."
	maintNoOla      = "Without Ola Hallengren's maintenance solution the job runs sp_updatestats, which samples each statistic and updates every one with any change; Ola's IndexOptimize does better on large tables: https://ola.hallengren.com"
	maintOla        = "Uses Ola Hallengren's IndexOptimize already in master, set up for statistics only as Erik Darling's job does it."
	maintIndexWarn  = "Index rebuilds write a lot of log and, without Enterprise edition, rebuild offline and block users of the table while they run: check that Sunday %s is a quiet hour and that the log has room."
	maintExpress    = "SQL Server Express has no Agent: kakapo cannot create the job. Copy the script and schedule it in Windows Task Scheduler (sqlcmd -E -i)."
	maintOlaOddName = "The database name has characters IndexOptimize reads as a list or a pattern, so the job uses sp_updatestats instead."
)

// maintenancePlan is the jobs maintenance-job creates.
type maintenancePlan struct {
	jobs      []KakapoJob
	time      string
	statsOnly bool
	ola       bool
	inputs    []FixInput
	options   map[string]string
	blocked   []string
}

func planMaintenance(f MaintenanceJobFacts, options map[string]string) maintenancePlan {
	p := maintenancePlan{time: DefaultMaintenanceTime, statsOnly: boolOption(options, OptionStatsOnly), options: map[string]string{}}
	p.ola = f.Ola && OlaDatabaseNameOK(f.Database)
	typed, given := options[OptionMaintTime]
	in := FixInput{Name: OptionMaintTime, Label: "Time", Value: p.time, Kind: "text", Help: maintTimeHelp}
	if v, ok := parseClock(typed); ok {
		p.time, in.Value = v, v
	} else if given {
		in.Value, in.Warning = strings.TrimSpace(typed), maintTimeWarn
		p.blocked = append(p.blocked, maintTimeWarn)
	}
	p.inputs = append(p.inputs, in)
	p.options[OptionMaintTime] = in.Value
	if !p.ola {
		p.statsOnly = true
	} else {
		p.inputs = append(p.inputs, FixInput{Name: OptionStatsOnly, Label: "Statistics only", Value: fmt.Sprint(p.statsOnly), Kind: "bool", Help: statsOnlyHelp})
		p.options[OptionStatsOnly] = fmt.Sprint(p.statsOnly)
	}

	date := f.ServerNow.Format("2006-01-02")
	operator := f.Operator
	if f.EngineEdition == 4 {
		operator = ""
	}
	indexJob := p.ola && !p.statsOnly && !f.exists(IndexJobName(f.Database))
	weeklyIndex := indexJob || (p.ola && f.exists(IndexJobName(f.Database)))
	if !f.exists(StatisticsJobName(f.Database)) {
		sc := KakapoJobSchedule{Name: StatisticsJobName(f.Database) + ", daily " + p.time, Freq: JobDaily, Clock: p.time}
		if weeklyIndex {
			// On Sunday the index job updates the statistics too.
			sc = KakapoJobSchedule{Name: StatisticsJobName(f.Database) + ", Monday to Saturday " + p.time, Freq: JobWeekly, Days: JobMonToSat, Clock: p.time}
		}
		cmd, how := updateStatsCommand(f.Database), "sp_updatestats"
		if p.ola {
			cmd, how = olaStatisticsCommand(f.Database), "IndexOptimize, statistics only"
		}
		p.jobs = append(p.jobs, KakapoJob{Name: StatisticsJobName(f.Database), Operator: operator, Schedule: sc,
			Description: KakapoJobDescription(date, f.Database, "statistics "+sc.words()+" ("+how+")"),
			Steps:       []KakapoJobStep{{Name: "Update statistics", Command: cmd}}})
	}
	if indexJob {
		sc := KakapoJobSchedule{Name: IndexJobName(f.Database) + ", Sunday " + p.time, Freq: JobWeekly, Days: JobSunday, Clock: p.time}
		p.jobs = append(p.jobs, KakapoJob{Name: IndexJobName(f.Database), Operator: operator, Schedule: sc,
			Description: KakapoJobDescription(date, f.Database, "indexes "+sc.words()+" (IndexOptimize: reorganise from 30%, rebuild from 50%, and statistics)"),
			Steps:       []KakapoJobStep{{Name: "Reorganise or rebuild indexes, update statistics", Command: olaIndexCommand(f.Database)}}})
	}
	return p
}

// script is what Express gets instead of jobs: the step commands to schedule with sqlcmd.
func (p maintenancePlan) script(f MaintenanceJobFacts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "-- kakapo: maintenance of %s on %s. SQL Server Express has no SQL Server Agent: schedule this in Windows Task Scheduler.\n", f.Database, f.ServerName)
	for _, j := range p.jobs {
		fmt.Fprintf(&b, "\n-- %s: run %s with sqlcmd -S %s -E -i <this file>\n", j.Name, j.Schedule.words(), f.ServerName)
		for _, s := range j.Steps {
			b.WriteString(kakapoStepCommand(s.Command) + "\nGO\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// BuildMaintenanceJobFix builds the maintenance-job preview with the options (time,
// statisticsOnly): the nightly statistics job (Ola's IndexOptimize when it is in master, else
// sp_updatestats) and, with statisticsOnly false and Ola there, the weekly index job; on Express a
// script to run by hand. ErrFixNotApplicable when kakapo's jobs exist already. A wrong time leaves
// the preview without changes.
func BuildMaintenanceJobFix(f MaintenanceJobFacts, options map[string]string) (FixPreview, Fix, error) {
	if f.exists(StatisticsJobName(f.Database)) && (!f.Ola || f.exists(IndexJobName(f.Database))) {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	plan := planMaintenance(f, options)
	express := f.EngineEdition == 4
	fix := suFix(FixMaintenanceJob)
	fix.Current, fix.Options = "no maintenance job", plan.options
	words := []string{}
	for _, j := range plan.jobs {
		words = append(words, j.Name+" ("+j.Schedule.words()+")")
	}
	fix.Target = strings.Join(words, " and ")
	p := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{},
		Inputs: plan.inputs, Options: plan.options, Express: express}
	p.Warnings = append(p.Warnings, plan.blocked...)
	if len(plan.jobs) == 0 && len(plan.blocked) == 0 {
		p.Warnings = append(p.Warnings, "kakapo's statistics job exists already; untick Statistics only to add the weekly index job.")
	}
	switch {
	case plan.ola:
		p.Warnings = append(p.Warnings, maintOla)
	case f.Ola:
		p.Warnings = append(p.Warnings, maintOlaOddName, maintNoOla)
	default:
		p.Warnings = append(p.Warnings, maintNoOla)
	}
	for _, j := range plan.jobs {
		if j.Name == IndexJobName(f.Database) {
			p.Warnings = append(p.Warnings, fmt.Sprintf(maintIndexWarn, plan.time))
		}
	}
	switch {
	case express:
		p.Warnings = append(p.Warnings, maintExpress)
	case f.AgentError == "" && f.AgentStatus != "" && f.AgentStatus != "Running":
		p.Warnings = append(p.Warnings, "SQL Server Agent is "+strings.ToLower(f.AgentStatus)+": the jobs will not run until it starts (MNT01).")
	}
	if express {
		p.Warnings = append(p.Warnings, "Needs sysadmin on "+f.ServerName+" for the Windows account that runs the scheduled task (sqlcmd -E logs in as that account).")
	} else {
		p.Warnings = append(p.Warnings, BackupFixPermission(f.ServerName))
	}
	if len(plan.blocked) > 0 || len(plan.jobs) == 0 {
		return p, fix, nil
	}
	for _, j := range plan.jobs {
		label := "Statistics job"
		if j.Name == IndexJobName(f.Database) {
			label = "Index job"
		}
		p.Changes = append(p.Changes, Drift{Setting: label, Current: "none", Target: j.Name + ", " + j.Schedule.words()})
	}
	told := noOperator
	switch {
	case express:
		told = "no one (Express has no Agent to send an email)"
	case f.Operator != "":
		told = f.Operator
	}
	p.Changes = append(p.Changes, Drift{Setting: "Tell on failure", Target: told})
	if express {
		p.Statement = plan.script(f)
		return p, fix, nil
	}
	p.Statement, p.Rollback = KakapoJobsBatch(plan.jobs), KakapoJobsDelete(KakapoJobNames(plan.jobs))
	return p, fix, nil
}

// MaintenanceJobNames are the jobs the maintenance-job preview creates (for the read-back).
func MaintenanceJobNames(f MaintenanceJobFacts, options map[string]string) []string {
	return KakapoJobNames(planMaintenance(f, options).jobs)
}

// MaintenanceJobDescription is the change log line of maintenance-job.
func MaintenanceJobDescription(fix Fix) string { return "Created the maintenance job(s) " + fix.Target }

// --- remove-shrink-step (MNT05) ---

// AgentJobStep is one step of an Agent job as msdb holds it, enough to create it again.
type AgentJobStep struct {
	ID              int
	Name            string
	Subsystem       string
	Command         string
	Database        string
	DatabaseUser    string
	OutputFile      string
	Proxy           string
	OnSuccessAction int // 1 quit with success, 2 quit with failure, 3 next step, 4 go to step
	OnSuccessStep   int
	OnFailAction    int
	OnFailStep      int
	RetryAttempts   int
	RetryInterval   int
	Flags           int
}

// ShrinkStepFacts is what remove-shrink-step reads about one job.
type ShrinkStepFacts struct {
	ServerName     string
	ProductVersion string
	Job            string
	Found          bool
	Enabled        bool
	StartStep      int
	Steps          []AgentJobStep
}

// Shrinks says whether a step's command shrinks a database or a file, as MNT05 reads it.
func (s AgentJobStep) Shrinks() bool {
	c := strings.ToUpper(s.Command)
	return strings.Contains(c, "SHRINKDATABASE") || strings.Contains(c, "SHRINKFILE")
}

func (f ShrinkStepFacts) step(id int) (AgentJobStep, bool) {
	for _, s := range f.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return AgentJobStep{}, false
}

// shrinkSteps are the job's steps that shrink, by number.
func (f ShrinkStepFacts) shrinkSteps() []AgentJobStep {
	out := []AgentJobStep{}
	for _, s := range f.Steps {
		if s.Shrinks() {
			out = append(out, s)
		}
	}
	return out
}

// addJobStepStatement creates a step again as it was read (the rollback of its removal); with the
// same step_id the steps after it move down one number, as before.
func addJobStepStatement(job string, s AgentJobStep) string {
	var b strings.Builder
	fmt.Fprintf(&b, "EXEC msdb.dbo.sp_add_jobstep @job_name = %s, @step_id = %d, @step_name = %s, @subsystem = %s,\n", nstr(job), s.ID, nstr(s.Name), nstr(s.Subsystem))
	fmt.Fprintf(&b, "    @on_success_action = %d, @on_success_step_id = %d, @on_fail_action = %d, @on_fail_step_id = %d,\n",
		s.OnSuccessAction, s.OnSuccessStep, s.OnFailAction, s.OnFailStep)
	fmt.Fprintf(&b, "    @retry_attempts = %d, @retry_interval = %d, @flags = %d", s.RetryAttempts, s.RetryInterval, s.Flags)
	if s.Database != "" {
		b.WriteString(", @database_name = " + nstr(s.Database))
	}
	if s.DatabaseUser != "" {
		b.WriteString(", @database_user_name = " + nstr(s.DatabaseUser))
	}
	if s.OutputFile != "" {
		b.WriteString(", @output_file_name = " + nstr(s.OutputFile))
	}
	if s.Proxy != "" {
		b.WriteString(", @proxy_name = " + nstr(s.Proxy))
	}
	b.WriteString(",\n    @command = " + nstr(s.Command) + ";")
	return b.String()
}

// ShrinkStepPermission is what remove-shrink-step needs and names when SQL Server refuses.
func ShrinkStepPermission(server string) string {
	return "Needs sysadmin on " + server + ", or to own the job."
}

// BuildRemoveShrinkStepFix builds the remove-shrink-step preview for the step option (the job's
// first shrink step when not given): sp_delete_jobstep, with the step created again from what was
// read as the rollback; when it is the job's only step, the job is disabled instead.
// ErrFixNotApplicable when no step of the job shrinks, or the job is disabled already. A step that
// does not shrink leaves the preview without changes.
func BuildRemoveShrinkStepFix(f ShrinkStepFacts, options map[string]string) (FixPreview, Fix, error) {
	shrinks := f.shrinkSteps()
	if !f.Found || !f.Enabled || len(shrinks) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	choices := make([]string, 0, len(shrinks))
	for _, s := range shrinks {
		choices = append(choices, strconv.Itoa(s.ID))
	}
	typed, given := options[OptionJobStep]
	typed = strings.TrimSpace(typed)
	step := shrinks[0]
	warn := ""
	if given && typed != "" {
		n, err := strconv.Atoi(typed)
		s, found := f.step(n)
		switch {
		case err != nil || !found:
			warn = fmt.Sprintf("The job %s has no step %s: choose one of its shrink steps (%s).", f.Job, typed, strings.Join(choices, ", "))
		case !s.Shrinks():
			warn = fmt.Sprintf("Step %d of %s does not shrink: choose one of its shrink steps (%s).", n, f.Job, strings.Join(choices, ", "))
		default:
			step = s
		}
	}
	value := strconv.Itoa(step.ID)
	if warn != "" {
		value = typed
	}
	opts := map[string]string{OptionJobName: f.Job, OptionJobStep: value}
	fix := suFix(FixRemoveShrinkStep)
	fix.Options = opts
	p := FixPreview{EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: opts,
		Inputs: []FixInput{{Name: OptionJobStep, Label: "Step", Value: value, Kind: "select", Options: choices, Warning: warn,
			Help: "The steps of " + f.Job + " that shrink a database or a file."}}}
	if warn != "" {
		fix.Setting = "Step " + value + " of " + f.Job
		p.Warnings = append(p.Warnings, warn, ShrinkStepPermission(f.ServerName))
		return p, fix, nil
	}
	job := nstr(f.Job)
	command := strings.Join(strings.Fields(step.Command), " ")
	if len(f.Steps) == 1 {
		fix.Setting, fix.Current, fix.Target = "Job "+f.Job, "enabled", "disabled"
		p.Changes = append(p.Changes, Drift{Setting: fix.Setting, Current: "enabled; its only step shrinks: " + left(command, 120), Target: "disabled"})
		p.Statement = "EXEC msdb.dbo.sp_update_job @job_name = " + job + ", @enabled = 0;"
		p.Rollback = "EXEC msdb.dbo.sp_update_job @job_name = " + job + ", @enabled = 1;"
		p.Warnings = append(p.Warnings,
			"The shrink is the job's only step, so kakapo disables the job instead of leaving it empty; delete it in SSMS when nobody needs it.",
			"If space must be reclaimed once, shrink once by hand in a quiet hour and rebuild the indexes afterwards.",
			ShrinkStepPermission(f.ServerName))
		return p, fix, nil
	}
	fix.Setting = fmt.Sprintf("Step %d of %s", step.ID, f.Job)
	fix.Current, fix.Target = fmt.Sprintf("%q: %s", step.Name, left(command, 120)), "removed"
	p.Changes = append(p.Changes, Drift{Setting: fix.Setting, Current: fix.Current, Target: fix.Target})
	p.Statement = fmt.Sprintf("EXEC msdb.dbo.sp_delete_jobstep @job_name = %s, @step_id = %d;", job, step.ID)
	rollback := addJobStepStatement(f.Job, step)
	if f.StartStep == step.ID {
		rollback += fmt.Sprintf("\nEXEC msdb.dbo.sp_update_job @job_name = %s, @start_step_id = %d;", job, step.ID)
	}
	p.Rollback = rollback
	p.Warnings = append(p.Warnings, "The job's other steps renumber; a step that jumps to this one by number breaks (shown below if any).")
	jumps := false
	for _, s := range f.Steps {
		if s.ID == step.ID {
			continue
		}
		if s.OnSuccessAction == 4 && s.OnSuccessStep == step.ID {
			jumps = true
			p.Warnings = append(p.Warnings, fmt.Sprintf("Step %d (%q) goes to step %d when it succeeds.", s.ID, s.Name, step.ID))
		}
		if s.OnFailAction == 4 && s.OnFailStep == step.ID {
			jumps = true
			p.Warnings = append(p.Warnings, fmt.Sprintf("Step %d (%q) goes to step %d when it fails.", s.ID, s.Name, step.ID))
		}
	}
	if f.StartStep == step.ID {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The job starts at step %d: after the removal it starts at the step that takes its number.", step.ID))
	}
	if jumps {
		p.Warnings = append(p.Warnings, "The rollback puts the step back as it was read; the jumps above are not restored by it.")
	}
	p.Warnings = append(p.Warnings,
		"If space must be reclaimed once, shrink once by hand in a quiet hour and rebuild the indexes afterwards.",
		ShrinkStepPermission(f.ServerName))
	return p, fix, nil
}

// RemoveShrinkStepReadBack says what the job looks like after the statement: disabled (when its
// only step shrank), or without the removed step (the same name and command).
func RemoveShrinkStepReadBack(disabled bool, removed AgentJobStep, after ShrinkStepFacts) (string, bool) {
	if !after.Found {
		return "the job is gone", false
	}
	if disabled {
		if after.Enabled {
			return "the job is still enabled", false
		}
		return "the job is disabled", true
	}
	for _, s := range after.Steps {
		if s.Name == removed.Name && s.Command == removed.Command {
			return fmt.Sprintf("the step is still there (step %d)", s.ID), false
		}
	}
	return fmt.Sprintf("step removed; the job has %d step(s)", len(after.Steps)), true
}

// RemoveShrinkStepDescription is the change log line of remove-shrink-step.
func RemoveShrinkStepDescription(fix Fix, server string) string {
	if fix.Target == "disabled" {
		return fix.Setting + " on " + server + ": disabled (its only step shrank)"
	}
	return fix.Setting + " on " + server + ": removed (" + fix.Current + ")"
}

// --- the findings' fixes ---

// upkeepActionFixes are the fixes the Upkeep card's actions offer on this read, one per finding
// (one per shrink step for remove-shrink-step), with the options the preview needs.
func upkeepActionFixes(f UpkeepFacts, x Finding) []Fix {
	switch x.FixID {
	case FixOperatorAlerts:
		fix := suFix(FixOperatorAlerts)
		fix.Current = alertsCurrent(f.Operators, len(f.MissingAlerts), len(f.SilentAlerts))
		fix.Target = "operator and alerts that mail it"
		return []Fix{fix}
	case FixMaintenanceJob:
		fix := suFix(FixMaintenanceJob)
		fix.Current, fix.Target = "no maintenance job", StatisticsJobName(f.Database)+" (daily "+DefaultMaintenanceTime+")"
		return []Fix{fix}
	case FixRemoveShrinkStep:
		out := []Fix{}
		for _, j := range f.ShrinkJobs {
			if j.Job != x.Object {
				continue
			}
			fix := suFix(FixRemoveShrinkStep)
			fix.Setting = fmt.Sprintf("Step %d of %s", j.Step, j.Job)
			fix.Current, fix.Target = left(j.Command, 120), "removed"
			fix.Options = map[string]string{OptionJobName: j.Job, OptionJobStep: strconv.Itoa(j.Step)}
			out = append(out, fix)
		}
		return out
	}
	return nil
}

func isUpkeepAction(id string) bool {
	return id == FixOperatorAlerts || id == FixMaintenanceJob || id == FixRemoveShrinkStep
}

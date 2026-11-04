package collect

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// deadlockQuery reads the xml_deadlock_report events of the system_health Extended Events session
// (docs/phase3e-contract.md, DLK01): from its file target (the session's current file's folder,
// every system_health*.xel there, so the rolled-over files are read too), or from its ring buffer
// target when the session has no file target or the files cannot be read. On 2017 and later the
// files are filtered on timestamp_utc to the window. The same read takes the session's
// sp_server_diagnostics_component_result events for the SYSTEM and IO_SUBSYSTEM components in
// state WARNING or ERROR (docs/phase3f-contract.md, item 21; sp_HealthParser's reads), in its own
// TRY so a failure there never costs the deadlocks. SELECTs only. Result sets: one row (source,
// error, file error, health error), then the deadlock events' XML, then the health events' XML.
//
// Source: DarlingData sp_HealthParser/sp_HealthParser.sql 3090-3135, 4700-4750 (MIT): the component,
// state and counter XPaths of the health events.
var deadlockQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @path nvarchar(520), @source nvarchar(20), @err nvarchar(2048), @fileErr nvarchar(2048), @healthErr nvarchar(2048), @running bit = 0;
DECLARE @events TABLE (event_xml nvarchar(max) NULL);
DECLARE @health TABLE (event_xml nvarchar(max) NULL);
DECLARE @major int = CONVERT(int, PARSENAME(CONVERT(varchar(32), SERVERPROPERTY('ProductVersion')), 4));
BEGIN TRY
    SELECT @running = 1 FROM sys.dm_xe_sessions WHERE name = N'system_health';
    IF @running = 0
        SET @err = N'The system_health Extended Events session is not running on this server.';
    ELSE
    BEGIN
        SELECT TOP (1) @path = CONVERT(xml, t.target_data).value('(EventFileTarget/File/@name)[1]', 'nvarchar(520)')
        FROM sys.dm_xe_session_targets AS t
        JOIN sys.dm_xe_sessions AS s ON s.address = t.event_session_address
        WHERE s.name = N'system_health' AND t.target_name = N'event_file';

        IF @path IS NOT NULL
        BEGIN TRY
            DECLARE @sep nchar(1) = CASE WHEN CHARINDEX(N'\', @path) > 0 THEN N'\' ELSE N'/' END;
            SET @path = LEFT(@path, LEN(@path) - CHARINDEX(@sep, REVERSE(@path)) + 1) + N'system_health*.xel';
            IF @major >= 14
                INSERT @events (event_xml)
                EXEC sys.sp_executesql N'/* kakapo */ SELECT CONVERT(nvarchar(max), event_data)
                    FROM sys.fn_xe_file_target_read_file(@p, NULL, NULL, NULL)
                    WHERE object_name = N''xml_deadlock_report''
                      AND timestamp_utc >= DATEADD(DAY, -@days, SYSUTCDATETIME());', N'@p nvarchar(520), @days int', @p = @path, @days = @days;
            ELSE
                INSERT @events (event_xml)
                SELECT CONVERT(nvarchar(max), event_data)
                FROM sys.fn_xe_file_target_read_file(@path, NULL, NULL, NULL)
                WHERE object_name = N'xml_deadlock_report';
            SET @source = N'file';
        END TRY
        BEGIN CATCH
            SET @fileErr = ERROR_MESSAGE();
            DELETE @events;
        END CATCH;

        IF @source = N'file'
        BEGIN TRY
            IF @major >= 14
                INSERT @health (event_xml)
                EXEC sys.sp_executesql N'/* kakapo */ SELECT CONVERT(nvarchar(max), x.e)
                    FROM (SELECT CONVERT(xml, event_data) AS e
                          FROM sys.fn_xe_file_target_read_file(@p, NULL, NULL, NULL)
                          WHERE object_name = N''sp_server_diagnostics_component_result''
                            AND timestamp_utc >= DATEADD(DAY, -@days, SYSUTCDATETIME())) AS x
                    WHERE x.e.exist(''/event/data[@name="component"]/text[. = "SYSTEM" or . = "IO_SUBSYSTEM"]'') = 1
                      AND x.e.exist(''/event/data[@name="state"]/text[. = "WARNING" or . = "ERROR"]'') = 1;', N'@p nvarchar(520), @days int', @p = @path, @days = @days;
            ELSE
                INSERT @health (event_xml)
                SELECT CONVERT(nvarchar(max), x.e)
                FROM (SELECT CONVERT(xml, event_data) AS e
                      FROM sys.fn_xe_file_target_read_file(@path, NULL, NULL, NULL)
                      WHERE object_name = N'sp_server_diagnostics_component_result') AS x
                WHERE x.e.exist('/event/data[@name="component"]/text[. = "SYSTEM" or . = "IO_SUBSYSTEM"]') = 1
                  AND x.e.exist('/event/data[@name="state"]/text[. = "WARNING" or . = "ERROR"]') = 1;
        END TRY
        BEGIN CATCH
            SET @healthErr = ERROR_MESSAGE();
            DELETE @health;
        END CATCH;

        IF @source IS NULL
        BEGIN
            INSERT @events (event_xml)
            SELECT CONVERT(nvarchar(max), e.x.query('.'))
            FROM (SELECT CONVERT(xml, t.target_data) AS td
                  FROM sys.dm_xe_session_targets AS t
                  JOIN sys.dm_xe_sessions AS s ON s.address = t.event_session_address
                  WHERE s.name = N'system_health' AND t.target_name = N'ring_buffer') AS r
            CROSS APPLY r.td.nodes('RingBufferTarget/event[@name="xml_deadlock_report"]') AS e(x);
            SET @source = N'ring-buffer';

            BEGIN TRY
                INSERT @health (event_xml)
                SELECT CONVERT(nvarchar(max), e.x.query('.'))
                FROM (SELECT CONVERT(xml, t.target_data) AS td
                      FROM sys.dm_xe_session_targets AS t
                      JOIN sys.dm_xe_sessions AS s ON s.address = t.event_session_address
                      WHERE s.name = N'system_health' AND t.target_name = N'ring_buffer') AS r
                CROSS APPLY r.td.nodes('RingBufferTarget/event[@name="sp_server_diagnostics_component_result"]') AS e(x)
                WHERE e.x.exist('data[@name="component"]/text[. = "SYSTEM" or . = "IO_SUBSYSTEM"]') = 1
                  AND e.x.exist('data[@name="state"]/text[. = "WARNING" or . = "ERROR"]') = 1;
            END TRY
            BEGIN CATCH
                SET @healthErr = ERROR_MESSAGE();
                DELETE @health;
            END CATCH;
        END;
    END;
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
SELECT @source, @err, @fileErr, @healthErr;
SELECT event_xml FROM @events;
SELECT event_xml FROM @health;`)

// ReadDeadlocks reads the deadlocks of the last days from the system_health session, SELECTs only,
// and parses each event's graph, and with them the session's SYSTEM and IO_SUBSYSTEM health
// events in state WARNING or ERROR. It says where it read from (file or ring-buffer); an error says
// why the session could not be read. Events whose XML cannot be parsed are left out, and so are
// the health events when only their read failed.
func ReadDeadlocks(ctx context.Context, db *sql.DB, days int) ([]domain.DeadlockEvent, []domain.HealthEvent, string, error) {
	rows, err := db.QueryContext(ctx, deadlockQuery, sql.Named("days", days))
	if err != nil {
		return nil, nil, "", friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, nil, "", friendlyError(err)
		}
		return nil, nil, "", errors.New("SQL Server returned nothing from the system_health session")
	}
	var source, readErr, fileErr, healthErr sql.NullString
	if err := rows.Scan(&source, &readErr, &fileErr, &healthErr); err != nil {
		return nil, nil, "", err
	}
	if readErr.String != "" {
		msg := readErr.String
		if fileErr.String != "" {
			msg = "the session files could not be read (" + fileErr.String + ") and then " + msg
		}
		return nil, nil, "", errors.New(msg)
	}
	if err := nextResultSet(rows, "deadlock events"); err != nil {
		return nil, nil, "", err
	}
	out := []domain.DeadlockEvent{}
	for rows.Next() {
		var x sql.NullString
		if err := rows.Scan(&x); err != nil {
			return nil, nil, "", err
		}
		if e, err := ParseDeadlockEvent(x.String); err == nil {
			out = append(out, e)
		}
	}
	if err := nextResultSet(rows, "health events"); err != nil {
		return nil, nil, "", err
	}
	health := []domain.HealthEvent{}
	for rows.Next() {
		var x sql.NullString
		if err := rows.Scan(&x); err != nil {
			return nil, nil, "", err
		}
		if e, err := ParseHealthEvent(x.String); err == nil {
			health = append(health, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, "", friendlyError(err)
	}
	return out, health, source.String, nil
}

// The parts of an sp_server_diagnostics_component_result event the health list reads: the
// component and state texts, and the attributes of the component's data element.
type xeHealthEvent struct {
	Timestamp string `xml:"timestamp,attr"`
	Data      []struct {
		Name  string `xml:"name,attr"`
		Text  string `xml:"text"`
		Value struct {
			Nodes []xhNode `xml:",any"`
		} `xml:"value"`
	} `xml:"data"`
}

type xhNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []xhNode   `xml:",any"`
}

func (n xhNode) attrs() map[string]string {
	m := map[string]string{}
	for _, a := range n.Attrs {
		m[strings.ToLower(a.Name.Local)] = a.Value
	}
	return m
}

// ParseHealthEvent reads one sp_server_diagnostics_component_result event of the SYSTEM or
// IO_SUBSYSTEM component: its time, component, state, and the counters the health list names
// (sp_HealthParser's XPaths: system/@nonYieldingTasksReported and the rest, ioSubsystem/@ioLatchTimeouts,
// @totalLongIos and longestPendingRequests/pendingRequest/@filePath and @duration).
func ParseHealthEvent(eventXML string) (domain.HealthEvent, error) {
	var ev xeHealthEvent
	if err := xml.Unmarshal([]byte(eventXML), &ev); err != nil {
		return domain.HealthEvent{}, fmt.Errorf("health event: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if err != nil {
		return domain.HealthEvent{}, fmt.Errorf("health event time %q: %w", ev.Timestamp, err)
	}
	var component, state string
	attrs, pending := map[string]string{}, []domain.PendingIO{}
	for _, d := range ev.Data {
		switch d.Name {
		case "component":
			component = strings.TrimSpace(d.Text)
		case "state":
			state = strings.TrimSpace(d.Text)
		case "data":
			for _, n := range d.Value.Nodes {
				if !strings.EqualFold(n.XMLName.Local, "system") && !strings.EqualFold(n.XMLName.Local, "ioSubsystem") {
					continue
				}
				attrs = n.attrs()
				for _, c := range n.Children {
					if !strings.EqualFold(c.XMLName.Local, "longestPendingRequests") {
						continue
					}
					for _, p := range c.Children {
						a := p.attrs()
						var ms int64
						fmt.Sscan(a["duration"], &ms)
						pending = append(pending, domain.PendingIO{File: a["filepath"], DurationMs: ms})
					}
				}
			}
		}
	}
	if component == "" {
		return domain.HealthEvent{}, errors.New("health event without a component")
	}
	return domain.HealthEvent{At: t.UTC(), Component: component, State: strings.ToLower(state),
		Detail: domain.HealthDetail(component, attrs, pending)}, nil
}

// The graph of an xml_deadlock_report event, as much of it as the report reads.
type xeEvent struct {
	Timestamp string `xml:"timestamp,attr"`
	Data      []struct {
		Name  string `xml:"name,attr"`
		Value struct {
			Deadlock *xdDeadlock `xml:"deadlock"`
		} `xml:"value"`
	} `xml:"data"`
}

type xdDeadlock struct {
	Victims []struct {
		ID string `xml:"id,attr"`
	} `xml:"victim-list>victimProcess"`
	Processes []xdProcess `xml:"process-list>process"`
	Resources struct {
		Items []xdResource `xml:",any"`
	} `xml:"resource-list"`
}

type xdProcess struct {
	ID       string    `xml:"id,attr"`
	LockMode string    `xml:"lockMode,attr"`
	DBName   string    `xml:"currentdbname,attr"`
	Login    string    `xml:"loginname,attr"`
	Host     string    `xml:"hostname,attr"`
	App      string    `xml:"clientapp,attr"`
	Frames   []xdFrame `xml:"executionStack>frame"`
	InputBuf string    `xml:"inputbuf"`
}

type xdFrame struct {
	ProcName string `xml:"procname,attr"`
	Text     string `xml:",chardata"`
}

type xdLockParty struct {
	ID   string `xml:"id,attr"`
	Mode string `xml:"mode,attr"`
}

type xdResource struct {
	XMLName    xml.Name
	ObjectName string        `xml:"objectname,attr"`
	HobtID     string        `xml:"hobtid,attr"`
	ObjectID   string        `xml:"associatedObjectId,attr"`
	Mode       string        `xml:"mode,attr"`
	Owners     []xdLockParty `xml:"owner-list>owner"`
	Waiters    []xdLockParty `xml:"waiter-list>waiter"`
}

// ParseDeadlockEvent reads one xml_deadlock_report event: its time, the victim and the winner
// (the first other process) with their procedures or statements, the objects and lock types, and
// the database.
func ParseDeadlockEvent(eventXML string) (domain.DeadlockEvent, error) {
	var ev xeEvent
	if err := xml.Unmarshal([]byte(eventXML), &ev); err != nil {
		return domain.DeadlockEvent{}, fmt.Errorf("deadlock event: %w", err)
	}
	var g *xdDeadlock
	for _, d := range ev.Data {
		if d.Value.Deadlock != nil {
			g = d.Value.Deadlock
			break
		}
	}
	if g == nil || len(g.Processes) == 0 {
		return domain.DeadlockEvent{}, errors.New("deadlock event without a graph")
	}
	t, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if err != nil {
		return domain.DeadlockEvent{}, fmt.Errorf("deadlock event time %q: %w", ev.Timestamp, err)
	}
	e := domain.DeadlockEvent{Time: t.UTC(), Objects: []string{}, LockModes: []string{}}

	victim := ""
	if len(g.Victims) > 0 {
		victim = g.Victims[0].ID
	}
	vi := 0
	for i, p := range g.Processes {
		if p.ID == victim {
			vi = i
			break
		}
	}
	e.Victim = g.Processes[vi].process()
	for i, p := range g.Processes {
		if i != vi {
			e.Winner = p.process()
			break
		}
	}

	seen := map[string]bool{}
	for _, r := range g.Resources.Items {
		db, object := splitObject(r.ObjectName)
		if object == "" {
			switch {
			case r.HobtID != "":
				object = "hobt " + r.HobtID
			case r.ObjectID != "":
				object = "object " + r.ObjectID
			}
		}
		if e.Database == "" && db != "" {
			e.Database = db
		}
		if object != "" && !seen[object] {
			seen[object] = true
			e.Objects = append(e.Objects, object)
		}
		e.LockModes = append(e.LockModes, lockText(r, object))
	}
	sort.Strings(e.Objects)
	if e.Database == "" {
		e.Database = e.Victim.Database
	}
	return e, nil
}

func (p xdProcess) process() domain.DeadlockProcess {
	out := domain.DeadlockProcess{ID: p.ID, LockMode: p.LockMode, Database: p.DBName, Login: p.Login, Host: p.Host, App: p.App}
	for _, f := range p.Frames {
		name := strings.TrimSpace(f.ProcName)
		if name == "" || strings.EqualFold(name, "adhoc") || strings.EqualFold(name, "unknown") {
			continue
		}
		_, out.Procedure = splitObject(name)
		break
	}
	for _, f := range p.Frames {
		if s := strings.TrimSpace(f.Text); s != "" {
			out.Statement = s
			break
		}
	}
	if out.Statement == "" {
		out.Statement = strings.TrimSpace(p.InputBuf)
	}
	return out
}

// splitObject splits "db.schema.name" into the database and "schema.name"; a two-part name has no
// database.
func splitObject(name string) (db, object string) {
	name = strings.TrimSpace(name)
	parts := strings.Split(name, ".")
	if len(parts) >= 3 {
		return parts[0], strings.Join(parts[len(parts)-2:], ".")
	}
	return "", name
}

// lockText is "keylock dbo.T: held X, wanted U".
func lockText(r xdResource, object string) string {
	held, wanted := distinctModes(r.Owners), distinctModes(r.Waiters)
	s := r.XMLName.Local
	if object != "" {
		s += " " + object
	}
	parts := []string{}
	if held != "" {
		parts = append(parts, "held "+held)
	} else if r.Mode != "" {
		parts = append(parts, "held "+r.Mode)
	}
	if wanted != "" {
		parts = append(parts, "wanted "+wanted)
	}
	if len(parts) > 0 {
		s += ": " + strings.Join(parts, ", ")
	}
	return s
}

func distinctModes(ps []xdLockParty) string {
	seen, out := map[string]bool{}, []string{}
	for _, p := range ps {
		if p.Mode != "" && !seen[p.Mode] {
			seen[p.Mode] = true
			out = append(out, p.Mode)
		}
	}
	return strings.Join(out, "/")
}

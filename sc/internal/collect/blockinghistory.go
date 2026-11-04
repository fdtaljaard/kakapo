package collect

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// blockingHistoryQuery reads a week of blocking from the system_health Extended Events session
// (docs/phase5d-contract.md, BLK01): the sp_server_diagnostics_component_result events whose
// QUERY_PROCESSING data holds blocked-process-report nodes, from the session's file target (every
// system_health*.xel in its folder, filtered on object_name, and on 2017 and later on timestamp_utc),
// or from its ring buffer when there is no file target or the files cannot be read, as the deadlock
// read does. Each blocked-process-report gives one row: the snapshot's time (UTC), the lock monitor's
// loop, the blocked process and the blocking process. The blocked process threshold and the
// database's READ COMMITTED SNAPSHOT setting are read in their own TRY. SELECTs only; every area has
// its own TRY and error column. Result sets: one row (source, error, file error, shred error, config
// error, threshold, database id, rcsi, the server clock's offset from UTC in minutes), then the pairs.
//
// Source: DarlingData sp_HumanEventsBlockViewer.sql 1355-1412 (MIT): the system_health file read on
// object_name = sp_server_diagnostics_component_result and its filter, the XPath
// //data[@name="data"]/value/queryProcessing/blockingTasks/blocked-process-report.
// Source: DarlingData sp_HumanEventsBlockViewer.sql 2092-2160 (MIT): the blocked-process and
// blocking-process XPaths (process/@spid, @ecid, @loginname, @hostname, @clientapp, @currentdbname,
// @currentdb, @waittime, @waitresource, @lockMode, @isolationlevel, @trancount, @status,
// @transactionname, @lasttranstarted, @lastbatchstarted, @clientoption1, inputbuf/text()) and the
// monitorLoop attribute. Dates are read as text and parsed in Go, so one odd value never costs the read.
var blockingHistoryQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @path nvarchar(520), @source nvarchar(20), @err nvarchar(2048), @fileErr nvarchar(2048), @shredErr nvarchar(2048), @configErr nvarchar(2048), @running bit = 0;
DECLARE @threshold int, @rcsi bit, @dbid int = DB_ID(@db), @offset int = DATEDIFF(MINUTE, GETUTCDATE(), SYSDATETIME());
DECLARE @since datetime2 = DATEADD(DAY, -@days, SYSUTCDATETIME());
DECLARE @events TABLE (event_xml xml NULL);
DECLARE @reports TABLE (event_utc datetime2 NULL, monitor_loop bigint NULL, bpr xml NULL);
DECLARE @pairs TABLE (
    event_utc datetime2 NULL, monitor_loop bigint NULL,
    bd_spid int NULL, bd_ecid int NULL, bd_login nvarchar(256) NULL, bd_host nvarchar(256) NULL, bd_app nvarchar(256) NULL,
    bd_dbname nvarchar(256) NULL, bd_dbid int NULL, bd_wait_ms bigint NULL, bd_wait_resource nvarchar(1024) NULL, bd_lock_mode nvarchar(10) NULL,
    bd_isolation nvarchar(50) NULL, bd_trancount int NULL, bd_status nvarchar(30) NULL, bd_tran_name nvarchar(1024) NULL,
    bd_last_tran nvarchar(40) NULL, bd_last_batch nvarchar(40) NULL, bd_xactid nvarchar(40) NULL, bd_option1 bigint NULL, bd_inputbuf nvarchar(4000) NULL,
    bg_spid int NULL, bg_ecid int NULL, bg_login nvarchar(256) NULL, bg_host nvarchar(256) NULL, bg_app nvarchar(256) NULL,
    bg_dbname nvarchar(256) NULL, bg_dbid int NULL, bg_status nvarchar(30) NULL, bg_isolation nvarchar(50) NULL, bg_trancount int NULL,
    bg_tran_name nvarchar(1024) NULL, bg_last_tran nvarchar(40) NULL, bg_last_batch nvarchar(40) NULL, bg_xactid nvarchar(40) NULL,
    bg_option1 bigint NULL, bg_inputbuf nvarchar(4000) NULL);
DECLARE @major int = CONVERT(int, PARSENAME(CONVERT(varchar(32), SERVERPROPERTY('ProductVersion')), 4));

BEGIN TRY
    SELECT @threshold = CONVERT(int, c.value_in_use) FROM sys.configurations AS c WHERE c.name = N'blocked process threshold (s)';
    SELECT @rcsi = d.is_read_committed_snapshot_on FROM sys.databases AS d WHERE d.name = @db;
END TRY
BEGIN CATCH
    SET @configErr = ERROR_MESSAGE();
END CATCH;

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
                EXEC sys.sp_executesql N'/* kakapo */ SELECT x.e
                    FROM (SELECT CONVERT(xml, event_data) AS e
                          FROM sys.fn_xe_file_target_read_file(@p, NULL, NULL, NULL)
                          WHERE object_name = N''sp_server_diagnostics_component_result''
                            AND CONVERT(datetime2, timestamp_utc) >= @s) AS x
                    WHERE x.e.exist(''//data[@name="data"]/value/queryProcessing/blockingTasks/blocked-process-report'') = 1;', N'@p nvarchar(520), @s datetime2', @p = @path, @s = @since;
            ELSE
                INSERT @events (event_xml)
                SELECT x.e
                FROM (SELECT CONVERT(xml, event_data) AS e
                      FROM sys.fn_xe_file_target_read_file(@path, NULL, NULL, NULL)
                      WHERE object_name = N'sp_server_diagnostics_component_result') AS x
                WHERE x.e.exist('//data[@name="data"]/value/queryProcessing/blockingTasks/blocked-process-report') = 1;
            SET @source = N'file';
        END TRY
        BEGIN CATCH
            SET @fileErr = ERROR_MESSAGE();
            DELETE @events;
        END CATCH;

        IF @source IS NULL
        BEGIN
            INSERT @events (event_xml)
            SELECT e.x.query('.')
            FROM (SELECT CONVERT(xml, t.target_data) AS td
                  FROM sys.dm_xe_session_targets AS t
                  JOIN sys.dm_xe_sessions AS s ON s.address = t.event_session_address
                  WHERE s.name = N'system_health' AND t.target_name = N'ring_buffer') AS r
            CROSS APPLY r.td.nodes('RingBufferTarget/event[@name="sp_server_diagnostics_component_result"]') AS e(x)
            WHERE e.x.exist('data[@name="data"]/value/queryProcessing/blockingTasks/blocked-process-report') = 1;
            SET @source = N'ring-buffer';
        END;
    END;
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;

BEGIN TRY
    INSERT @reports (event_utc, monitor_loop, bpr)
    SELECT t.ts, r.x.value('@monitorLoop', 'bigint'), r.x.query('.')
    FROM @events AS e
    CROSS APPLY (SELECT e.event_xml.value('(/event/@timestamp)[1]', 'datetime2')) AS t(ts)
    CROSS APPLY e.event_xml.nodes('/event/data[@name="data"]/value/queryProcessing/blockingTasks/blocked-process-report') AS r(x)
    WHERE t.ts >= @since;

    INSERT @pairs
    SELECT r.event_utc, r.monitor_loop,
           bd.value('(process/@spid)[1]', 'int'), bd.value('(process/@ecid)[1]', 'int'),
           bd.value('(process/@loginname)[1]', 'nvarchar(256)'), bd.value('(process/@hostname)[1]', 'nvarchar(256)'),
           bd.value('(process/@clientapp)[1]', 'nvarchar(256)'), bd.value('(process/@currentdbname)[1]', 'nvarchar(256)'),
           bd.value('(process/@currentdb)[1]', 'int'), bd.value('(process/@waittime)[1]', 'bigint'),
           bd.value('(process/@waitresource)[1]', 'nvarchar(1024)'), bd.value('(process/@lockMode)[1]', 'nvarchar(10)'),
           bd.value('(process/@isolationlevel)[1]', 'nvarchar(50)'), bd.value('(process/@trancount)[1]', 'int'),
           bd.value('(process/@status)[1]', 'nvarchar(30)'), bd.value('(process/@transactionname)[1]', 'nvarchar(1024)'),
           bd.value('(process/@lasttranstarted)[1]', 'nvarchar(40)'), bd.value('(process/@lastbatchstarted)[1]', 'nvarchar(40)'),
           bd.value('(process/@xactid)[1]', 'nvarchar(40)'),
           bd.value('(process/@clientoption1)[1]', 'bigint'), LEFT(bd.value('(process/inputbuf/text())[1]', 'nvarchar(max)'), 4000),
           bg.value('(process/@spid)[1]', 'int'), bg.value('(process/@ecid)[1]', 'int'),
           bg.value('(process/@loginname)[1]', 'nvarchar(256)'), bg.value('(process/@hostname)[1]', 'nvarchar(256)'),
           bg.value('(process/@clientapp)[1]', 'nvarchar(256)'), bg.value('(process/@currentdbname)[1]', 'nvarchar(256)'),
           bg.value('(process/@currentdb)[1]', 'int'), bg.value('(process/@status)[1]', 'nvarchar(30)'),
           bg.value('(process/@isolationlevel)[1]', 'nvarchar(50)'), bg.value('(process/@trancount)[1]', 'int'),
           bg.value('(process/@transactionname)[1]', 'nvarchar(1024)'), bg.value('(process/@lasttranstarted)[1]', 'nvarchar(40)'),
           bg.value('(process/@lastbatchstarted)[1]', 'nvarchar(40)'), bg.value('(process/@xactid)[1]', 'nvarchar(40)'),
           bg.value('(process/@clientoption1)[1]', 'bigint'), LEFT(bg.value('(process/inputbuf/text())[1]', 'nvarchar(max)'), 4000)
    FROM @reports AS r
    OUTER APPLY r.bpr.nodes('/blocked-process-report/blocked-process') AS bdn(bd)
    OUTER APPLY r.bpr.nodes('/blocked-process-report/blocking-process') AS bgn(bg)
    -- A head of a chain is written as a blocked-process with trancount > 0 and an empty blocking-process
    -- (system_health keeps the head only): kept, with a NULL blocker.
    WHERE bd.exist('process/@spid') = 1
      AND (bg.exist('process/@spid') = 1 OR bd.exist('process[@trancount > 0]') = 1);
END TRY
BEGIN CATCH
    SET @shredErr = ERROR_MESSAGE();
    DELETE @pairs;
END CATCH;

SELECT @source, @err, @fileErr, @shredErr, @configErr, @threshold, @dbid, @rcsi, @offset, @major;
SELECT event_utc, monitor_loop,
       bd_spid, bd_ecid, bd_login, bd_host, bd_app, bd_dbname, bd_dbid, bd_wait_ms, bd_wait_resource, bd_lock_mode,
       bd_isolation, bd_trancount, bd_status, bd_tran_name, bd_last_tran, bd_last_batch, bd_xactid, bd_option1, bd_inputbuf,
       bg_spid, bg_ecid, bg_login, bg_host, bg_app, bg_dbname, bg_dbid, bg_status, bg_isolation, bg_trancount,
       bg_tran_name, bg_last_tran, bg_last_batch, bg_xactid, bg_option1, bg_inputbuf
FROM @pairs ORDER BY event_utc;`)

// blockingNamesQuery names, in the database, what the reports point at (@res, an XML list built by
// blockingNamesXML): the table behind a KEY lock's hobt (sys.partitions), an OBJECT lock's object or a
// procedure an RPC input buffer names (sys.objects), a PAGE or RID lock's page (sys.dm_db_page_info,
// SQL Server 2019 and later), and the Query Store hash of a blocker's exact statement text. Each in its
// own TRY with an error column; SELECTs only. Result sets: one row (names error, pages error, hash
// error), then (key, schema.name), then (index, query hash).
var blockingNamesQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @namesErr nvarchar(2048), @pagesErr nvarchar(2048), @hashErr nvarchar(2048), @x xml;
DECLARE @names TABLE (k nvarchar(100) NOT NULL, name nvarchar(300) NULL);
DECLARE @hashes TABLE (i int NOT NULL, query_hash varchar(20) NULL);
DECLARE @in nvarchar(300) = QUOTENAME(@db) + N'.sys.sp_executesql';
DECLARE @major int = CONVERT(int, PARSENAME(CONVERT(varchar(32), SERVERPROPERTY('ProductVersion')), 4));

BEGIN TRY
    SET @x = CONVERT(xml, @res);
    INSERT @names (k, name)
    EXEC @in N'/* kakapo */ SELECT r.x.value(''@k'', ''nvarchar(100)''), s.name + N''.'' + o.name
        FROM @x.nodes(''/r/h'') AS r(x)
        JOIN sys.partitions AS p ON p.hobt_id = r.x.value(''@id'', ''bigint'')
        JOIN sys.objects AS o ON o.object_id = p.object_id
        JOIN sys.schemas AS s ON s.schema_id = o.schema_id
        UNION ALL
        SELECT r.x.value(''@k'', ''nvarchar(100)''), s.name + N''.'' + o.name
        FROM @x.nodes(''/r/o'') AS r(x)
        JOIN sys.objects AS o ON o.object_id = r.x.value(''@id'', ''int'')
        JOIN sys.schemas AS s ON s.schema_id = o.schema_id;', N'@x xml', @x = @x;
END TRY
BEGIN CATCH
    SET @namesErr = ERROR_MESSAGE();
END CATCH;

IF @major >= 15 AND @x.exist('/r/p') = 1
BEGIN TRY
    INSERT @names (k, name)
    EXEC @in N'/* kakapo */ SELECT r.x.value(''@k'', ''nvarchar(100)''), s.name + N''.'' + o.name
        FROM @x.nodes(''/r/p'') AS r(x)
        CROSS APPLY sys.dm_db_page_info(DB_ID(), r.x.value(''@f'', ''int''), r.x.value(''@pg'', ''int''), N''LIMITED'') AS pi
        JOIN sys.objects AS o ON o.object_id = pi.object_id
        JOIN sys.schemas AS s ON s.schema_id = o.schema_id;', N'@x xml', @x = @x;
END TRY
BEGIN CATCH
    SET @pagesErr = ERROR_MESSAGE();
END CATCH;

IF @major >= 13 AND @x.exist('/r/q') = 1
BEGIN TRY
    INSERT @hashes (i, query_hash)
    EXEC @in N'/* kakapo */ SELECT q2.i, MIN(CONVERT(varchar(20), q.query_hash, 1))
        FROM (SELECT r.x.value(''@i'', ''int'') AS i, r.x.value(''.'', ''nvarchar(max)'') AS txt FROM @x.nodes(''/r/q'') AS r(x)) AS q2
        JOIN sys.query_store_query_text AS t ON t.query_sql_text = q2.txt
        JOIN sys.query_store_query AS q ON q.query_text_id = t.query_text_id
        GROUP BY q2.i;', N'@x xml', @x = @x;
END TRY
BEGIN CATCH
    SET @hashErr = ERROR_MESSAGE();
END CATCH;

SELECT @namesErr, @pagesErr, @hashErr;
SELECT k, name FROM @names;
SELECT i, query_hash FROM @hashes;`)

// blockingHashTextsMax is how many distinct blocker statements the Query Store match takes.
const blockingHashTextsMax = 200

// ReadBlockingHistory reads the blocking of the last days from the system_health session for one
// database, SELECTs only, and names what the reports point at in that database. It fills everything of
// the inputs but when it was read and how long it took. An error says why the session could not be read;
// a part that failed (the threshold, the names, the hashes) is a note instead.
func ReadBlockingHistory(ctx context.Context, db *sql.DB, database string, days int) (domain.BlockingHistoryInputs, error) {
	in := domain.BlockingHistoryInputs{Database: database, Days: days, Reports: []domain.BlockedProcessReport{}, Notes: []string{}}
	rows, err := db.QueryContext(ctx, blockingHistoryQuery, sql.Named("db", database), sql.Named("days", days))
	if err != nil {
		return in, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return in, friendlyError(err)
		}
		return in, errors.New("SQL Server returned nothing from the system_health session")
	}
	var (
		source, readErr, fileErr, shredErr, configErr sql.NullString
		threshold, dbID, offset, major                sql.NullInt64
		rcsi                                          sql.NullBool
	)
	if err := rows.Scan(&source, &readErr, &fileErr, &shredErr, &configErr, &threshold, &dbID, &rcsi, &offset, &major); err != nil {
		return in, err
	}
	if readErr.String != "" {
		msg := readErr.String
		if fileErr.String != "" {
			msg = "the session files could not be read (" + fileErr.String + ") and then " + msg
		}
		return in, errors.New(msg)
	}
	in.Source, in.ThresholdSeconds, in.DatabaseID, in.OffsetMinutes, in.RCSI =
		source.String, int(threshold.Int64), int(dbID.Int64), int(offset.Int64), rcsi.Bool
	if fileErr.String != "" {
		in.Notes = append(in.Notes, "The session files could not be read ("+fileErr.String+"), so this is the ring buffer, which keeps only the last few hours.")
	} else if in.Source == domain.DeadlockSourceRingBuffer {
		in.Notes = append(in.Notes, "The system_health session has no file target, so this is its ring buffer, which keeps only the last few hours.")
	}
	if shredErr.String != "" {
		in.Notes = append(in.Notes, "The blocked process reports could not be read: "+shredErr.String)
	}
	if configErr.String != "" {
		in.Notes = append(in.Notes, "The blocked process threshold could not be read: "+configErr.String)
	}
	if err := nextResultSet(rows, "blocked process reports"); err != nil {
		return in, err
	}
	for rows.Next() {
		r, err := scanBlockedReport(rows)
		if err != nil {
			return in, err
		}
		in.Reports = append(in.Reports, r)
	}
	if err := rows.Err(); err != nil {
		return in, friendlyError(err)
	}
	rows.Close()

	res, texts, pages := blockingNamesXML(in.Reports, in.DatabaseID)
	if res == "<r></r>" {
		return in, nil
	}
	if pages && major.Int64 > 0 && major.Int64 < 15 {
		in.Notes = append(in.Notes, "Page and row locks can only be named on SQL Server 2019 and later; on this server they show as page numbers.")
	}
	names, hashes, notes, err := readBlockingNames(ctx, db, database, res)
	if err != nil {
		in.Notes = append(in.Notes, "The locked tables could not be named: "+err.Error())
		return in, nil
	}
	in.Notes = append(in.Notes, notes...)
	applyBlockingNames(in.Reports, in.DatabaseID, names, hashes, texts)
	return in, nil
}

func scanBlockedReport(rows *sql.Rows) (domain.BlockedProcessReport, error) {
	var (
		at                                                             sql.NullTime
		loop, bdWait, bdOpt, bgOpt                                     sql.NullInt64
		bdSPID, bdECID, bdDBID, bdTran, bgSPID, bgECID, bgDBID, bgTran sql.NullInt64
		bdLogin, bdHost, bdApp, bdDB, bdRes, bdMode, bdIso, bdStatus   sql.NullString
		bdTranName, bdLastTran, bdLastBatch, bdXact, bdBuf             sql.NullString
		bgLogin, bgHost, bgApp, bgDB, bgStatus, bgIso, bgTranName      sql.NullString
		bgLastTran, bgLastBatch, bgXact, bgBuf                         sql.NullString
	)
	if err := rows.Scan(&at, &loop,
		&bdSPID, &bdECID, &bdLogin, &bdHost, &bdApp, &bdDB, &bdDBID, &bdWait, &bdRes, &bdMode,
		&bdIso, &bdTran, &bdStatus, &bdTranName, &bdLastTran, &bdLastBatch, &bdXact, &bdOpt, &bdBuf,
		&bgSPID, &bgECID, &bgLogin, &bgHost, &bgApp, &bgDB, &bgDBID, &bgStatus, &bgIso, &bgTran,
		&bgTranName, &bgLastTran, &bgLastBatch, &bgXact, &bgOpt, &bgBuf); err != nil {
		return domain.BlockedProcessReport{}, err
	}
	// A head the snapshot names without a blocker (an empty blocking-process) reads as Blocking.SPID 0.
	xact, _ := strconv.ParseInt(strings.TrimSpace(bgXact.String), 10, 64)
	bdXactID, _ := strconv.ParseInt(strings.TrimSpace(bdXact.String), 10, 64)
	return domain.BlockedProcessReport{
		EventUTC:    time.Date(at.Time.Year(), at.Time.Month(), at.Time.Day(), at.Time.Hour(), at.Time.Minute(), at.Time.Second(), at.Time.Nanosecond(), time.UTC),
		MonitorLoop: loop.Int64,
		Blocked: domain.BlockedProcess{SPID: int(bdSPID.Int64), ECID: int(bdECID.Int64), Login: bdLogin.String, Host: bdHost.String,
			Program: bdApp.String, Database: bdDB.String, DatabaseID: int(bdDBID.Int64), WaitMs: bdWait.Int64, WaitResource: bdRes.String,
			LockMode: bdMode.String, Isolation: bdIso.String, TranCount: int(bdTran.Int64), Status: bdStatus.String, TranName: bdTranName.String,
			LastTranStarted: ParseReportTime(bdLastTran.String), LastBatchStarted: ParseReportTime(bdLastBatch.String),
			XactID: bdXactID, ClientOption1: bdOpt.Int64, InputBuffer: bdBuf.String},
		Blocking: domain.BlockedProcess{SPID: int(bgSPID.Int64), ECID: int(bgECID.Int64), Login: bgLogin.String, Host: bgHost.String,
			Program: bgApp.String, Database: bgDB.String, DatabaseID: int(bgDBID.Int64), Status: bgStatus.String, Isolation: bgIso.String,
			TranCount: int(bgTran.Int64), TranName: bgTranName.String, LastTranStarted: ParseReportTime(bgLastTran.String),
			LastBatchStarted: ParseReportTime(bgLastBatch.String), XactID: xact, ClientOption1: bgOpt.Int64, InputBuffer: bgBuf.String},
	}, nil
}

// ParseReportTime reads a time of a blocked process report ("2026-11-03T14:05:01.233"), the server's
// local clock; the zero time when it is empty or not a time.
func ParseReportTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// blockingNamesXML is what blockingNamesQuery names, as <r>: <h k id/> per hobt, <o k id/> per object
// (an OBJECT lock, or a procedure an RPC input buffer names), <p k f pg/> per page, <q i>text</q> per
// distinct blocker statement, all in this database. texts are the statements by their index; pages says
// whether a page or row lock is among them.
func blockingNamesXML(reports []domain.BlockedProcessReport, dbID int) (res string, texts []string, pages bool) {
	keys := map[string]bool{}
	seenText := map[string]bool{}
	var b strings.Builder
	b.WriteString("<r>")
	add := func(key string) {
		if keys[key] {
			return
		}
		keys[key] = true
		kind, rest, _ := strings.Cut(key, ":")
		switch kind {
		case "h":
			fmt.Fprintf(&b, `<h k="%s" id="%s"/>`, key, rest)
		case "o":
			fmt.Fprintf(&b, `<o k="%s" id="%s"/>`, key, rest)
		case "p":
			file, page, _ := strings.Cut(rest, ":")
			fmt.Fprintf(&b, `<p k="%s" f="%s" pg="%s"/>`, key, file, page)
			pages = true
		}
	}
	for _, r := range reports {
		if db, key := domain.WaitResourceDatabase(r.Blocked.WaitResource); key != "" && db == dbID {
			add(key)
		}
		for _, p := range []domain.BlockedProcess{r.Blocked, r.Blocking} {
			if db, obj, ok := domain.ProcInputBuffer(p.InputBuffer); ok && db == dbID {
				add(fmt.Sprintf("o:%d", obj))
			}
		}
		if t := strings.TrimSpace(r.Holder().InputBuffer); t != "" && !seenText[t] && len(texts) < blockingHashTextsMax {
			if _, _, ok := domain.ProcInputBuffer(t); !ok {
				seenText[t] = true
				texts = append(texts, t)
			}
		}
	}
	for i, t := range texts {
		fmt.Fprintf(&b, `<q i="%d">`, i)
		_ = xml.EscapeText(&b, []byte(xmlSafe(t)))
		b.WriteString("</q>")
	}
	b.WriteString("</r>")
	return b.String(), texts, pages
}

// xmlSafe drops the characters XML 1.0 cannot carry.
func xmlSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || r >= 0x10000 && r <= 0x10FFFF {
			return r
		}
		return -1
	}, s)
}

func readBlockingNames(ctx context.Context, db *sql.DB, database, res string) (names map[string]string, hashes map[int]string, notes []string, err error) {
	rows, err := db.QueryContext(ctx, blockingNamesQuery, sql.Named("db", database), sql.Named("res", res))
	if err != nil {
		return nil, nil, nil, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, nil, nil, friendlyError(err)
		}
		return nil, nil, nil, errors.New("SQL Server returned nothing")
	}
	var namesErr, pagesErr, hashErr sql.NullString
	if err := rows.Scan(&namesErr, &pagesErr, &hashErr); err != nil {
		return nil, nil, nil, err
	}
	if namesErr.String != "" {
		notes = append(notes, "Some locked tables could not be named: "+namesErr.String)
	}
	if pagesErr.String != "" {
		notes = append(notes, "Page and row locks could not be named (sys.dm_db_page_info needs VIEW DATABASE STATE): "+pagesErr.String)
	}
	if hashErr.String != "" {
		notes = append(notes, "The blockers' statements could not be matched in Query Store: "+hashErr.String)
	}
	if err := nextResultSet(rows, "names"); err != nil {
		return nil, nil, nil, err
	}
	names = map[string]string{}
	for rows.Next() {
		var k, name sql.NullString
		if err := rows.Scan(&k, &name); err != nil {
			return nil, nil, nil, err
		}
		if name.String != "" {
			names[k.String] = name.String
		}
	}
	if err := nextResultSet(rows, "query hashes"); err != nil {
		return nil, nil, nil, err
	}
	hashes = map[int]string{}
	for rows.Next() {
		var i sql.NullInt64
		var h sql.NullString
		if err := rows.Scan(&i, &h); err != nil {
			return nil, nil, nil, err
		}
		if h.String != "" {
			hashes[int(i.Int64)] = h.String
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, friendlyError(err)
	}
	sort.Strings(notes)
	return names, hashes, notes, nil
}

// applyBlockingNames puts the names on the reports: the object behind each wait resource of this
// database, the procedure behind each RPC input buffer, the Query Store hash of each blocker statement.
func applyBlockingNames(reports []domain.BlockedProcessReport, dbID int, names map[string]string, hashes map[int]string, texts []string) {
	byText := map[string]string{}
	for i, t := range texts {
		if h := hashes[i]; h != "" {
			byText[t] = h
		}
	}
	proc := func(p *domain.BlockedProcess) {
		if db, obj, ok := domain.ProcInputBuffer(p.InputBuffer); ok && db == dbID {
			p.Procedure = names[fmt.Sprintf("o:%d", obj)]
		}
	}
	for i := range reports {
		r := &reports[i]
		if db, key := domain.WaitResourceDatabase(r.Blocked.WaitResource); key != "" && db == dbID {
			r.Blocked.Object = names[key]
		}
		proc(&r.Blocked)
		proc(&r.Blocking)
		if r.Blocking.SPID == 0 {
			r.Blocked.QueryHash = byText[strings.TrimSpace(r.Blocked.InputBuffer)]
		} else {
			r.Blocking.QueryHash = byText[strings.TrimSpace(r.Blocking.InputBuffer)]
		}
	}
}

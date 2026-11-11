package collect

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"strconv"
	"strings"

	"sqlmonitor/internal/domain"
)

// The reads of the Indexes page (docs/phase5t-contract.md, section 1): the query shapes of the last
// 7 days with their most recently run plan, and a cheap catalogue read of the indexes found in them.
// SELECTs only. Measuring an index (SAMPLED) stays IndexShape's job, on demand.

// indexCandidatesQuery runs inside the database (EXEC [db].sys.sp_executesql) with @limit, the plan
// size in characters above which a plan is not read. Result sets: one row (Query Store's
// actual_state_desc, an error the read itself raised), then at most 100 query shapes by total duration
// over the last 7 days (grouped by query_hash, kakapo's own statements and the noise patterns left
// out as QRY01 does) with the text cut to 200 characters, the runs, the total duration in
// microseconds, the total logical reads, the last run, and the XML of the plan that ran last.
var indexCandidatesQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @state nvarchar(60), @err nvarchar(2048), @now datetimeoffset = SYSDATETIMEOFFSET();
CREATE TABLE #top (query_hash binary(8) NOT NULL PRIMARY KEY, elapsed bigint NOT NULL, execs bigint NOT NULL, reads bigint NOT NULL, last_run datetimeoffset NULL);
BEGIN TRY
    SELECT @state = CONVERT(nvarchar(60), actual_state_desc) FROM sys.database_query_store_options;
    IF @state IN (N'READ_WRITE', N'READ_ONLY')
        INSERT #top (query_hash, elapsed, execs, reads, last_run)
        SELECT TOP (` + strconv.Itoa(domain.LensMaxQueries) + `) q.query_hash,
               SUM(CONVERT(bigint, rs.avg_duration * rs.count_executions)),
               SUM(CONVERT(bigint, rs.count_executions)),
               SUM(CONVERT(bigint, rs.avg_logical_io_reads * rs.count_executions)),
               MAX(rs.last_execution_time)
        FROM sys.query_store_runtime_stats AS rs
        JOIN sys.query_store_runtime_stats_interval AS rsi ON rsi.runtime_stats_interval_id = rs.runtime_stats_interval_id
        JOIN sys.query_store_plan AS p ON p.plan_id = rs.plan_id
        JOIN sys.query_store_query AS q ON q.query_id = p.query_id
        JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
        WHERE rsi.start_time >= DATEADD(DAY, -7, @now)
          AND ` + noiseFilter("qt.query_sql_text") + `
        GROUP BY q.query_hash
        ORDER BY SUM(CONVERT(bigint, rs.avg_duration * rs.count_executions)) DESC;
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
SELECT @state, @err, DATEPART(TZoffset, @now);
SELECT CONVERT(varchar(18), t.query_hash, 1), LEFT(x.query_sql_text, 200), t.execs, t.elapsed, t.reads, t.last_run,
       CASE WHEN DATALENGTH(x.query_plan) / 2 <= @limit THEN x.query_plan END
FROM #top AS t
CROSS APPLY (SELECT TOP (1) qp.query_plan, qt.query_sql_text
             FROM sys.query_store_plan AS qp
             JOIN sys.query_store_query AS q ON q.query_id = qp.query_id
             JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
             WHERE q.query_hash = t.query_hash
             ORDER BY qp.last_execution_time DESC) AS x
ORDER BY t.elapsed DESC;`)

// IndexCandidates reads the query shapes the Indexes page parses for indexes read in full, into the
// facts: Query Store's state ("" when it could not be read), an error the read itself raised (a
// permission), the server's offset from UTC and the queries. err is for a connection that failed.
func IndexCandidates(ctx context.Context, db *sql.DB, database string) (domain.LensFacts, error) {
	f := domain.LensFacts{Database: database, Queries: []domain.LensQueryFact{}}
	rows, err := db.QueryContext(ctx, Tag(safeSession+"EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt, N'@limit bigint', @limit = @limit;"),
		sql.Named("stmt", indexCandidatesQuery), sql.Named("limit", int64(planLimitChars)))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing about Query Store")
	}
	var st, msg sql.NullString
	var offset sql.NullInt64
	if err := rows.Scan(&st, &msg, &offset); err != nil {
		return f, err
	}
	if err := nextResultSet(rows, "query shapes"); err != nil {
		return f, err
	}
	for rows.Next() {
		var q domain.LensQueryFact
		var text, plan sql.NullString
		var last sql.NullTime
		if err := rows.Scan(&q.Hash, &text, &q.Runs, &q.ElapsedUs, &q.Reads, &last, &plan); err != nil {
			return f, err
		}
		q.Text, q.Plan = lensText(text.String), plan.String
		if last.Valid {
			q.LastRun = last.Time.UTC()
		}
		f.Queries = append(f.Queries, q)
	}
	if err := rows.Err(); err != nil {
		return f, friendlyError(err)
	}
	f.QueryStoreState, f.Error, f.UTCOffsetMin = st.String, msg.String, int(offset.Int64)
	return f, nil
}

// lensText is a query's text on one line, without the parameter list SQL Server puts in front of a
// parameterised statement: "(@1 varchar(8000))SELECT ..." becomes "SELECT ...".
func lensText(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "(@") {
		depth := 0
		for i, c := range text {
			switch c {
			case '(':
				depth++
			case ')':
				if depth--; depth == 0 {
					text = text[i+1:]
					return strings.Join(strings.Fields(text), " ")
				}
			}
		}
	}
	return strings.Join(strings.Fields(text), " ")
}

// indexBasicsQuery runs inside the database with @items, an XML list <i s="schema" n="table" x="index"/>:
// the recovery model and version, then per index whether its table and it exist, its type, whether it
// is disabled, ALLOW_PAGE_LOCKS, the fill factor, and its pages from the allocation units (in-row data
// pages, and every page it uses). No physical-stats read, and no permission beyond the catalogue.
var indexBasicsQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @err nvarchar(2048), @rec nvarchar(60);
DECLARE @names TABLE (schema_name nvarchar(128) NOT NULL, table_name nvarchar(128) NOT NULL, index_name nvarchar(128) NOT NULL);
BEGIN TRY
    SELECT @rec = recovery_model_desc FROM sys.databases WHERE database_id = DB_ID();
    INSERT @names (schema_name, table_name, index_name)
    SELECT DISTINCT x.i.value('@s', 'nvarchar(128)'), x.i.value('@n', 'nvarchar(128)'), x.i.value('@x', 'nvarchar(128)')
    FROM (SELECT CONVERT(xml, @items) AS doc) AS d
    CROSS APPLY d.doc.nodes('/i') AS x(i);
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
SELECT @err, @rec, CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64));
SELECT n.schema_name, n.table_name, n.index_name,
       CAST(CASE WHEN o.id IS NULL THEN 0 ELSE 1 END AS bit), CAST(CASE WHEN i.index_id IS NULL THEN 0 ELSE 1 END AS bit),
       i.name, i.type, i.is_disabled, i.allow_page_locks, i.fill_factor, ps.in_row, ps.used
FROM @names AS n
CROSS APPLY (SELECT OBJECT_ID(QUOTENAME(n.schema_name) + N'.' + QUOTENAME(n.table_name), N'U') AS id) AS o
LEFT JOIN sys.indexes AS i ON i.object_id = o.id AND i.name = n.index_name
OUTER APPLY (SELECT SUM(CASE WHEN a.type = 1 THEN a.data_pages ELSE 0 END) AS in_row, SUM(a.used_pages) AS used
             FROM sys.partitions AS p
             JOIN sys.allocation_units AS a ON (a.type IN (1, 3) AND a.container_id = p.hobt_id) OR (a.type = 2 AND a.container_id = p.partition_id)
             WHERE p.object_id = i.object_id AND p.index_id = i.index_id) AS ps;`)

// IndexBasics reads the chosen indexes from the catalogue, with the recovery model and version for the
// preview's warnings. An error the read raised is in the returned error; the facts hold what was read.
func IndexBasics(ctx context.Context, db *sql.DB, database string, items []domain.BatchIndex) (domain.BatchFacts, error) {
	f := domain.BatchFacts{Basics: []domain.IndexBasicsFact{}}
	var x strings.Builder
	for _, it := range items {
		x.WriteString(`<i s="`)
		_ = xml.EscapeText(&x, []byte(it.Schema))
		x.WriteString(`" n="`)
		_ = xml.EscapeText(&x, []byte(it.Table))
		x.WriteString(`" x="`)
		_ = xml.EscapeText(&x, []byte(it.Index))
		x.WriteString(`"/>`)
	}
	rows, err := db.QueryContext(ctx, Tag(safeSession+"EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt, N'@items nvarchar(max)', @items = @items;"),
		sql.Named("stmt", indexBasicsQuery), sql.Named("items", x.String()))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing about the indexes")
	}
	var msg, rec, version sql.NullString
	if err := rows.Scan(&msg, &rec, &version); err != nil {
		return f, err
	}
	f.Recovery, f.ProductVersion = rec.String, version.String
	if err := nextResultSet(rows, "indexes"); err != nil {
		return f, err
	}
	for rows.Next() {
		var b domain.IndexBasicsFact
		var name sql.NullString
		var typ, ff, inRow, used sql.NullInt64
		var disabled, locks sql.NullBool
		if err := rows.Scan(&b.Schema, &b.Table, &b.Index, &b.TableFound, &b.IndexFound, &name, &typ, &disabled, &locks, &ff, &inRow, &used); err != nil {
			return f, err
		}
		if name.Valid {
			b.Index = name.String
		}
		b.Type, b.FillFactor, b.Disabled, b.PageLocks = int(typ.Int64), int(ff.Int64), disabled.Bool, locks.Bool
		b.Pages, b.UsedPages = inRow.Int64, used.Int64
		f.Basics = append(f.Basics, b)
	}
	if err := rows.Err(); err != nil {
		return f, friendlyError(err)
	}
	if msg.String != "" {
		return f, errors.New(msg.String)
	}
	return f, nil
}

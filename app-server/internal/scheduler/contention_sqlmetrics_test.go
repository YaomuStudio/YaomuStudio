package scheduler

// 本文件是 SQLite 争用测量的测试侧计时设施，不进入产品构建。
//
// 目标：在不改产品写路径的前提下，按 SQL 类别记录调用次数、读取行数、影响行数
// 与耗时，并把“等待单个连接池连接”“事务持有”“事务内 SQL”“提交 fsync”分开。
// 只记录类别名与计数，不记录 SQL 正文、参数或任何业务内容。
//
// modernc.org/sqlite 没有公开 per-statement trace 钩子，因此用 driver 包装层
// 包住已迁移的同一个 SQLite 文件。生产打开参数（WAL、foreign_keys、busy_timeout、
// MaxOpenConns=1）由夹具显式重放并断言，见 contention_test.go。

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	sqlite "modernc.org/sqlite"
)

type sqlTraceKey struct{}

// sqlTrace 汇总一次被测操作（一个 tick 或一个并发请求）的 SQL 计时。
//
// 每条 trace 只由一个执行 goroutine 使用：driver 回调发生在调用方 goroutine，
// 因此不需要内部加锁；beginCh 用 sync.Once 关闭，供并发用例等待事务真正开始。
type sqlTrace struct {
	started   time.Time
	firstSQL  time.Time
	beginAt   time.Time
	commitAt  time.Time
	commitEnd time.Time
	end       time.Time

	beginDur  time.Duration
	commitDur time.Duration
	queryDur  time.Duration
	scanDur   time.Duration
	execDur   time.Duration
	sqlTotal  time.Duration

	sqlBeforeTx time.Duration
	sqlInTx     time.Duration
	rowsBefore  int64
	rowsInTx    int64
	rows        int64
	affected    int64
	calls       int64
	busyErrors  int

	inTx      bool
	sawBegin  bool
	sawCommit bool

	beginOnce sync.Once
	beginCh   chan struct{}

	cats map[string]*sqlCategoryStat
}

type sqlCategoryStat struct {
	calls    int64
	rows     int64
	affected int64
	query    time.Duration
	scan     time.Duration
	exec     time.Duration
}

func newSQLTrace() *sqlTrace {
	return &sqlTrace{
		beginCh: make(chan struct{}),
		cats:    make(map[string]*sqlCategoryStat),
	}
}

func traceFromContext(ctx context.Context) *sqlTrace {
	if ctx == nil {
		return nil
	}
	tr, _ := ctx.Value(sqlTraceKey{}).(*sqlTrace)
	return tr
}

func (tr *sqlTrace) cat(name string) *sqlCategoryStat {
	stat := tr.cats[name]
	if stat == nil {
		stat = &sqlCategoryStat{}
		tr.cats[name] = stat
	}
	return stat
}

// markFirstSQL 必须在真正进入 driver 调用之前调用：firstSQL 到操作起点之间的
// 时间就是数据库/sql 连接池排队（含 Service 写锁）时间。
func (tr *sqlTrace) markFirstSQL() {
	if tr.firstSQL.IsZero() {
		tr.firstSQL = time.Now()
	}
}

func (tr *sqlTrace) addPhase(d time.Duration, rows int64) {
	if tr.inTx {
		tr.sqlInTx += d
		tr.rowsInTx += rows
	} else {
		tr.sqlBeforeTx += d
		tr.rowsBefore += rows
	}
}

func (tr *sqlTrace) noteErr(err error) {
	if isBusyError(err) {
		tr.busyErrors++
	}
}

func (tr *sqlTrace) onBegin(start time.Time, d time.Duration, err error) {
	tr.calls++
	tr.beginDur += d
	tr.sqlTotal += d
	stat := tr.cat("tx.begin")
	stat.calls++
	stat.exec += d
	// 无论成功与否都放行等待者，避免事务失败时并发用例永久阻塞。
	tr.beginOnce.Do(func() { close(tr.beginCh) })
	if err != nil {
		tr.noteErr(err)
		return
	}
	tr.sawBegin = true
	tr.beginAt = start
	tr.inTx = true
}

func (tr *sqlTrace) onCommit(start time.Time, d time.Duration, err error) {
	tr.calls++
	tr.commitDur += d
	tr.sqlTotal += d
	stat := tr.cat("tx.commit")
	stat.calls++
	stat.exec += d
	if err != nil {
		tr.noteErr(err)
		tr.inTx = false
		return
	}
	tr.sawCommit = true
	tr.commitEnd = start.Add(d)
	tr.inTx = false
}

func (tr *sqlTrace) onRollback(d time.Duration, err error) {
	tr.calls++
	tr.sqlTotal += d
	tr.commitDur += d
	stat := tr.cat("tx.rollback")
	stat.calls++
	stat.exec += d
	if err != nil {
		tr.noteErr(err)
	}
	tr.inTx = false
}

func (tr *sqlTrace) onQuery(cat string, d time.Duration, err error) {
	tr.calls++
	tr.queryDur += d
	tr.sqlTotal += d
	stat := tr.cat(cat)
	stat.calls++
	stat.query += d
	tr.addPhase(d, 0)
	tr.noteErr(err)
}

func (tr *sqlTrace) onExec(cat string, d time.Duration, affected int64, err error) {
	tr.calls++
	tr.execDur += d
	tr.sqlTotal += d
	tr.affected += affected
	stat := tr.cat(cat)
	stat.calls++
	stat.exec += d
	stat.affected += affected
	tr.addPhase(d, 0)
	tr.noteErr(err)
}

func (tr *sqlTrace) onScan(cat string, d time.Duration, err error) {
	tr.scanDur += d
	tr.sqlTotal += d
	stat := tr.cat(cat)
	stat.scan += d
	if err == nil {
		tr.rows++
		stat.rows++
		tr.addPhase(0, 1)
	} else if err != io.EOF {
		tr.noteErr(err)
	}
}

func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "sqlite_busy")
}

// classifySQL 只用固定关键词把语句归入稳定类别；不返回也不记录语句正文。
func classifySQL(query string) string {
	q := strings.Join(strings.Fields(strings.ToUpper(query)), " ")
	switch {
	case strings.Contains(q, "TASKS.STATUS IN (?, ?)"):
		return "task.waiting_candidates"
	case strings.HasPrefix(q, "SELECT ID, PROJECT_ID") && strings.Contains(q, "FROM TASKS WHERE ID = ?"):
		return "task.get"
	case strings.Contains(q, "SELECT STATUS, STATE_VERSION FROM TASKS"):
		return "task.select_status_version"
	case strings.Contains(q, "SELECT STATE_VERSION FROM TASKS"):
		return "task.select_progress_version"
	case strings.HasPrefix(q, "UPDATE TASKS SET STATUS"):
		return "task.update_status"
	case strings.HasPrefix(q, "UPDATE TASKS SET PROGRESS") ||
		strings.HasPrefix(q, "UPDATE TASKS SET PROGRESS = ?"):
		return "task.update_progress"
	case strings.Contains(q, "FROM WORKFLOW_RUNS WHERE ID = ?"):
		return "run.get"
	case strings.Contains(q, "FROM RUN_NODES"):
		return "run.nodes"
	case strings.Contains(q, "FROM ATTEMPTS"):
		return "attempt.select"
	case strings.HasPrefix(q, "UPDATE ATTEMPTS"):
		return "attempt.update"
	case strings.HasPrefix(q, "INSERT INTO EVENTS"):
		return "event.insert"
	case strings.HasPrefix(q, "INSERT INTO ATTEMPTS"):
		return "attempt.insert"
	case strings.HasPrefix(q, "INSERT INTO RECEIPTS") || strings.Contains(q, "FROM RECEIPTS"):
		return "receipt"
	}
	op := "other"
	if fields := strings.Fields(q); len(fields) > 0 {
		op = strings.ToLower(fields[0])
	}
	table := "unknown"
	for _, prefix := range []string{"FROM ", "UPDATE ", "INTO "} {
		if idx := strings.Index(q, prefix); idx >= 0 {
			rest := strings.Fields(q[idx+len(prefix):])
			if len(rest) > 0 {
				table = strings.ToLower(rest[0])
			}
			break
		}
	}
	return op + "." + table
}

// ---- driver 包装 ----

type traceConnector struct {
	dsn string
	drv *sqlite.Driver
}

func (c *traceConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &traceConn{Conn: conn}, nil
}

func (c *traceConnector) Driver() driver.Driver { return c.drv }

type traceConn struct {
	driver.Conn
}

func (c *traceConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &traceStmt{Stmt: stmt, cat: classifySQL(query)}, nil
}

func (c *traceConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	preparer, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		return c.Prepare(query)
	}
	stmt, err := preparer.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &traceStmt{Stmt: stmt, cat: classifySQL(query)}, nil
}

func (c *traceConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tr := traceFromContext(ctx)
	if tr != nil {
		tr.markFirstSQL()
	}
	start := time.Now()
	var (
		tx  driver.Tx
		err error
	)
	if begin, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = begin.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin()
	}
	if tr != nil {
		tr.onBegin(start, time.Since(start), err)
	}
	if err != nil {
		return nil, err
	}
	return &traceTx{Tx: tx, tr: tr}, nil
}

func (c *traceConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	tr := traceFromContext(ctx)
	cat := classifySQL(query)
	if tr != nil {
		tr.markFirstSQL()
	}
	start := time.Now()
	res, err := c.exec(ctx, query, args)
	elapsed := time.Since(start)
	var affected int64
	if res != nil {
		if n, rowsErr := res.RowsAffected(); rowsErr == nil {
			affected = n
		}
	}
	if tr != nil {
		tr.onExec(cat, elapsed, affected, err)
	}
	return res, err
}

func (c *traceConn) exec(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	stmt, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	if execCtx, ok := stmt.(driver.StmtExecContext); ok {
		return execCtx.ExecContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return stmt.Exec(values)
}

func (c *traceConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	tr := traceFromContext(ctx)
	cat := classifySQL(query)
	if tr != nil {
		tr.markFirstSQL()
	}
	start := time.Now()
	rows, err := c.query(ctx, query, args)
	if tr != nil {
		tr.onQuery(cat, time.Since(start), err)
	}
	if err != nil || rows == nil {
		return rows, err
	}
	return &traceRows{Rows: rows, tr: tr, cat: cat}, nil
}

func (c *traceConn) query(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := c.Conn.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	stmt, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	if queryCtx, ok := stmt.(driver.StmtQueryContext); ok {
		return queryCtx.QueryContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return stmt.Query(values)
}

func (c *traceConn) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *traceConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *traceConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

type traceStmt struct {
	driver.Stmt
	cat string
}

func (s *traceStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	tr := traceFromContext(ctx)
	if tr != nil {
		tr.markFirstSQL()
	}
	start := time.Now()
	var (
		res driver.Result
		err error
	)
	if execCtx, ok := s.Stmt.(driver.StmtExecContext); ok {
		res, err = execCtx.ExecContext(ctx, args)
	} else {
		values := make([]driver.Value, len(args))
		for i, arg := range args {
			values[i] = arg.Value
		}
		res, err = s.Stmt.Exec(values)
	}
	elapsed := time.Since(start)
	var affected int64
	if res != nil {
		if n, rowsErr := res.RowsAffected(); rowsErr == nil {
			affected = n
		}
	}
	if tr != nil {
		tr.onExec(s.cat, elapsed, affected, err)
	}
	return res, err
}

func (s *traceStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	tr := traceFromContext(ctx)
	if tr != nil {
		tr.markFirstSQL()
	}
	start := time.Now()
	var (
		rows driver.Rows
		err  error
	)
	if queryCtx, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err = queryCtx.QueryContext(ctx, args)
	} else {
		values := make([]driver.Value, len(args))
		for i, arg := range args {
			values[i] = arg.Value
		}
		rows, err = s.Stmt.Query(values)
	}
	if tr != nil {
		tr.onQuery(s.cat, time.Since(start), err)
	}
	if err != nil || rows == nil {
		return rows, err
	}
	return &traceRows{Rows: rows, tr: tr, cat: s.cat}, nil
}

type traceRows struct {
	driver.Rows
	tr  *sqlTrace
	cat string
}

func (r *traceRows) Next(dest []driver.Value) error {
	start := time.Now()
	err := r.Rows.Next(dest)
	if r.tr != nil {
		r.tr.onScan(r.cat, time.Since(start), err)
	}
	return err
}

func (r *traceRows) ColumnTypeDatabaseTypeName(index int) string {
	if typed, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return typed.ColumnTypeDatabaseTypeName(index)
	}
	return ""
}

func (r *traceRows) ColumnTypeLength(index int) (int64, bool) {
	if typed, ok := r.Rows.(driver.RowsColumnTypeLength); ok {
		return typed.ColumnTypeLength(index)
	}
	return 0, false
}

func (r *traceRows) ColumnTypeNullable(index int) (bool, bool) {
	if typed, ok := r.Rows.(driver.RowsColumnTypeNullable); ok {
		return typed.ColumnTypeNullable(index)
	}
	return false, false
}

func (r *traceRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	if typed, ok := r.Rows.(driver.RowsColumnTypePrecisionScale); ok {
		return typed.ColumnTypePrecisionScale(index)
	}
	return 0, 0, false
}

func (r *traceRows) ColumnTypeScanType(index int) reflect.Type {
	if typed, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return typed.ColumnTypeScanType(index)
	}
	return reflect.TypeOf(new(any)).Elem()
}

type traceTx struct {
	driver.Tx
	tr *sqlTrace
}

func (t *traceTx) Commit() error {
	start := time.Now()
	err := t.Tx.Commit()
	if t.tr != nil {
		t.tr.onCommit(start, time.Since(start), err)
	}
	return err
}

func (t *traceTx) Rollback() error {
	start := time.Now()
	err := t.Tx.Rollback()
	if t.tr != nil {
		t.tr.onRollback(time.Since(start), err)
	}
	return err
}

// ---- 统计辅助 ----

type durationSummary struct {
	p50 time.Duration
	p95 time.Duration
	max time.Duration
}

func summarizeDurations(samples []time.Duration) durationSummary {
	if len(samples) == 0 {
		return durationSummary{}
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return durationSummary{
		p50: percentileDuration(sorted, 0.50),
		p95: percentileDuration(sorted, 0.95),
		max: sorted[len(sorted)-1],
	}
}

func percentileDuration(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted))*p+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func summarizeInt64(samples []int64) (p50, max int64) {
	if len(samples) == 0 {
		return 0, 0
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := len(sorted)/2 - 1
	if index < 0 {
		index = 0
	}
	return sorted[index], sorted[len(sorted)-1]
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.3f", float64(d)/float64(time.Millisecond))
}

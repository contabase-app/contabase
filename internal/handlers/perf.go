package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

var _perfEnabled = initPerfEnabled()

func initPerfEnabled() bool {
	v := strings.TrimSpace(os.Getenv("APP_DEBUG"))
	return v == "1" || strings.EqualFold(v, "true")
}

func PerfEnabled() bool { return _perfEnabled }

func perfReqID() string {
	if !_perfEnabled {
		return ""
	}
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func PerfReqID() string { return perfReqID() }

func perfStep(reqID, handler, step string, elapsed time.Duration) {
	if !_perfEnabled {
		return
	}
	log.Printf("[PERF] %s | %s.%s | %s", reqID, handler, step, elapsed.Round(time.Microsecond))
}

func PerfStep(reqID, handler, step string, elapsed time.Duration) { perfStep(reqID, handler, step, elapsed) }

func perfRequest(reqID string, r *http.Request, elapsed time.Duration, responseBytes int) {
	if !_perfEnabled {
		return
	}
	log.Printf("[PERF] %s | %s %s?%s | hx_req=%s hx_trigger=%s hx_url=%s | total=%s bytes=%d",
		reqID,
		r.Method, r.URL.Path, r.URL.RawQuery,
		r.Header.Get("HX-Request"),
		r.Header.Get("HX-Trigger"),
		r.Header.Get("HX-Current-URL"),
		elapsed.Round(time.Microsecond),
		responseBytes,
	)
}

func PerfRequest(reqID string, r *http.Request, elapsed time.Duration, responseBytes int) {
	perfRequest(reqID, r, elapsed, responseBytes)
}

type perfDBSnap struct {
	WaitCount       int64
	WaitDuration    time.Duration
	InUse           int
	OpenConnections int
}

func dbSnap(db *sql.DB) perfDBSnap {
	s := db.Stats()
	return perfDBSnap{
		WaitCount:       s.WaitCount,
		WaitDuration:    s.WaitDuration,
		InUse:           s.InUse,
		OpenConnections: s.OpenConnections,
	}
}

func DbSnap(db *sql.DB) perfDBSnap { return dbSnap(db) }

func perfDBDelta(reqID, handler, step string, before, after perfDBSnap) {
	if !_perfEnabled {
		return
	}
	waitDelta := after.WaitCount - before.WaitCount
	waitDurDelta := after.WaitDuration - before.WaitDuration
	log.Printf("[PERF] %s | %s.%s | db_wait_delta=%d db_wait_dur_delta=%s db_in_use=%d->%d db_open=%d",
		reqID, handler, step,
		waitDelta, waitDurDelta.Round(time.Microsecond),
		before.InUse, after.InUse,
		after.OpenConnections,
	)
}

func PerfDBDelta(reqID, handler, step string, before, after perfDBSnap) {
	perfDBDelta(reqID, handler, step, before, after)
}

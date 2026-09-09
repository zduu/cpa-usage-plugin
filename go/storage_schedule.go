package main

import "time"

const storageRetryDelay = 100 * time.Millisecond

// No pending work means no timer. Queue arrival and shutdown are independent
// select cases, and reconfiguration replaces the worker only after draining it.
func (w *storageWorkerState) nextDeadline(now time.Time) time.Time {
	if w == nil {
		return time.Time{}
	}
	var next time.Time
	add := func(due, retry time.Time) {
		if due.IsZero() {
			return
		}
		if due.Before(now) {
			due = now
		}
		if retry.After(due) {
			due = retry
		}
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	if w.writer != nil && w.buffered > 0 {
		due := now
		if w.cfg.flushInterval > 0 && !w.lastFlush.IsZero() {
			due = w.lastFlush.Add(w.cfg.flushInterval)
		}
		add(due, w.flushRetryAt)
	}
	if w.file != nil && w.unsyncedRecords > 0 {
		if w.cfg.syncRecordInterval > 0 && w.unsyncedRecords >= int64(w.cfg.syncRecordInterval) {
			add(now, w.syncRetryAt)
		} else if w.cfg.syncInterval > 0 {
			base := w.lastSync
			if base.IsZero() {
				base = w.firstUnsyncedRecord
			}
			if !base.IsZero() {
				add(base.Add(w.cfg.syncInterval), w.syncRetryAt)
			}
		}
	}
	if w.snapshotRecords > 0 {
		if w.cfg.snapshotRecordInterval > 0 && w.snapshotRecords >= int64(w.cfg.snapshotRecordInterval) {
			add(now, w.snapshotRetryAt)
		} else if w.cfg.snapshotInterval > 0 {
			base := w.lastSnapshot
			if base.IsZero() {
				base = w.firstSnapshotRecord
			}
			if !base.IsZero() {
				add(base.Add(w.cfg.snapshotInterval), w.snapshotRetryAt)
			}
		}
	}
	return next
}

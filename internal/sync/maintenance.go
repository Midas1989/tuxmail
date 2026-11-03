// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"time"

	"github.com/Midas1989/Mailclient/internal/store"
)

// MaintenanceReport summarizes a maintenance run.
type MaintenanceReport struct {
	// Deleted is the number of orphaned messages marked deleted.
	Deleted int
	// Unthreaded is the number of message keys removed from their
	// conversation (no live or orphaned row left).
	Unthreaded int
	// PurgedOps is the number of completed operations removed.
	PurgedOps int64
	Optimized bool
}

// CollectGarbage marks messages orphaned for longer than OrphanMaxAge as
// deleted (KONZEPT 6.5) and removes keys without any remaining row from
// the conversation graph (their relations are kept).
func (m *Manager) CollectGarbage(ctx context.Context) (MaintenanceReport, error) {
	var rep MaintenanceReport
	gone, err := m.st.GCOrphanedMessages(ctx, m.cfg.OrphanMaxAge, m.now())
	if err != nil {
		return rep, err
	}
	rep.Deleted = len(gone)
	done := map[string]bool{}
	var convs []int64
	for _, msg := range gone {
		key := store.MessageKey(msg)
		convs = append(convs, msg.ConversationID)
		if err := m.st.UnindexMessage(ctx, msg.ID); err != nil {
			return rep, err
		}
		if done[key] {
			continue
		}
		done[key] = true
		alive, err := m.st.KeyHasRows(ctx, key)
		if err != nil {
			return rep, err
		}
		if alive {
			continue
		}
		res, err := m.engine.RemoveMessage(ctx, key)
		if err != nil {
			return rep, err
		}
		rep.Unthreaded++
		convs = append(convs, resultConvs(res)...)
	}
	if err := m.st.RefreshConversations(ctx, convs...); err != nil {
		return rep, err
	}
	m.publishConversations(ctx, convs...)
	return rep, nil
}

// RunMaintenance runs garbage collection, purges completed operations older
// than a week and optimizes the full-text index.
func (m *Manager) RunMaintenance(ctx context.Context) (MaintenanceReport, error) {
	rep, err := m.CollectGarbage(ctx)
	if err != nil {
		return rep, err
	}
	if rep.PurgedOps, err = m.st.PurgeDoneOps(ctx, m.now().Add(-7*24*time.Hour)); err != nil {
		return rep, err
	}
	if err := m.st.OptimizeSearchIndex(ctx); err != nil {
		return rep, err
	}
	rep.Optimized = true
	return rep, nil
}

func (m *Manager) maintenanceLoop(ctx context.Context) {
	gc := time.NewTimer(m.cfg.MaintenanceDelay)
	opt := time.NewTimer(m.cfg.MaintenanceDelay + time.Minute)
	defer gc.Stop()
	defer opt.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gc.C:
			rep, err := m.CollectGarbage(ctx)
			if err != nil && ctx.Err() == nil {
				m.log.Warn("sync: garbage collection", "err", err)
			} else if rep.Deleted > 0 {
				m.log.Info("sync: garbage collection", "deleted", rep.Deleted, "unthreaded", rep.Unthreaded)
			}
			if _, err := m.st.PurgeDoneOps(ctx, m.now().Add(-7*24*time.Hour)); err != nil && ctx.Err() == nil {
				m.log.Warn("sync: purge operations", "err", err)
			}
			gc.Reset(m.cfg.GCInterval)
		case <-opt.C:
			if err := m.st.OptimizeSearchIndex(ctx); err != nil && ctx.Err() == nil {
				m.log.Warn("sync: optimize search index", "err", err)
			}
			opt.Reset(m.cfg.OptimizeInterval)
		}
	}
}

package monitor

import (
	"context"
	"fmt"
	"sync"

	"github.com/robfig/cron/v3"

	"numind-server/internal/pkg/log"
)

// MonitorScheduler manages per-user cron jobs for crawling and briefing generation.
type MonitorScheduler struct {
	cron       *cron.Cron
	entries    map[uint][]cron.EntryID // userID → entryIDs
	mu         sync.RWMutex
	monitorBiz *MonitorBiz
}

// NewMonitorScheduler creates a new scheduler backed by robfig/cron.
func NewMonitorScheduler(mb *MonitorBiz) *MonitorScheduler {
	return &MonitorScheduler{
		cron:       cron.New(),
		entries:    make(map[uint][]cron.EntryID),
		monitorBiz: mb,
	}
}

// Start loads all active configs from the DB and registers cron jobs for each user.
// The DB query is intentionally done outside the lock to avoid holding the mutex during I/O.
func (s *MonitorScheduler) Start(ctx context.Context) error {
	configs, err := s.monitorBiz.store.Monitor().ListAllActiveConfigs(ctx)
	if err != nil {
		return fmt.Errorf("MonitorScheduler.Start: %w", err)
	}

	s.mu.Lock()
	for _, cfg := range configs {
		if err := s.registerUserJobs(cfg.UserID, cfg.CrawlCron, cfg.BriefingCron, cfg.BriefingType); err != nil {
			log.Warnw("Failed to register monitor jobs for user", "userID", cfg.UserID, "error", err)
		}
	}
	s.mu.Unlock()

	s.cron.Start()
	log.Infow("Monitor scheduler started", "users", len(configs))
	return nil
}

// Stop gracefully stops the cron scheduler, waiting for running jobs to finish.
func (s *MonitorScheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done() // wait for running jobs to finish
	log.Infow("Monitor scheduler stopped")
}

// RefreshUser removes existing jobs for a user and re-registers them with new cron expressions.
func (s *MonitorScheduler) RefreshUser(userID uint, crawlCron, briefingCron, briefingType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove old entries
	if ids, ok := s.entries[userID]; ok {
		for _, id := range ids {
			s.cron.Remove(id)
		}
		delete(s.entries, userID)
	}

	return s.registerUserJobs(userID, crawlCron, briefingCron, briefingType)
}

// registerUserJobs adds crawl and briefing cron jobs for the given user.
// Must be called with s.mu held (or during Start before cron is running).
func (s *MonitorScheduler) registerUserJobs(userID uint, crawlCron, briefingCron, briefingType string) error {
	var ids []cron.EntryID

	// Crawl job
	uid := userID // capture for closure
	crawlID, err := s.cron.AddFunc(crawlCron, func() {
		ctx := context.Background()
		// Get all active bloggers for this user and crawl them
		bloggers, listErr := s.monitorBiz.store.Monitor().ListActiveBloggersByUser(ctx, uid)
		if listErr != nil {
			log.Errorw("Scheduled crawl: failed to list bloggers", "userID", uid, "error", listErr)
			return
		}
		if len(bloggers) == 0 {
			return
		}
		bloggerIDs := make([]uint, len(bloggers))
		for i, b := range bloggers {
			bloggerIDs[i] = b.ID
		}
		if crawlErr := s.monitorBiz.CrawlBloggers(ctx, uid, bloggerIDs); crawlErr != nil {
			log.Errorw("Scheduled crawl failed", "userID", uid, "error", crawlErr)
		}
	})
	if err != nil {
		return fmt.Errorf("register crawl job: %w", err)
	}
	ids = append(ids, crawlID)

	// Briefing job
	bt := briefingType
	briefingID, err := s.cron.AddFunc(briefingCron, func() {
		ctx := context.Background()
		if _, briefErr := s.monitorBiz.GenerateUserBriefing(ctx, uid, bt); briefErr != nil {
			log.Errorw("Scheduled briefing failed", "userID", uid, "type", bt, "error", briefErr)
		}
	})
	if err != nil {
		s.cron.Remove(crawlID)
		return fmt.Errorf("register briefing job: %w", err)
	}
	ids = append(ids, briefingID)

	s.entries[userID] = ids
	log.Infow("Registered monitor jobs", "userID", userID, "crawl", crawlCron, "briefing", briefingCron)
	return nil
}

package stages

import (
	"fmt"
	"time"

	"persisto/src/utils"

	"go.uber.org/zap"
)

func SetupStageMonitor(getDatabases func() []Database) {
	if !utils.Config.Settings.AutoStageMovement {
		utils.Logger.Info("Auto stage movements disabled, not starting monitoring.")
		return
	}

	go func() {
		utils.Logger.Info(
			"Starting stage monitor service.",
			zap.Int("timeout", utils.Config.Settings.StageTimeoutSeconds),
		)

		// TODO: setup an event listener rather than continuously locking the database to check for changes
		ticker := time.NewTicker(time.Duration(utils.Config.Settings.StageTimeoutSeconds/2) * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			databases := getDatabases()
			MonitorAndDemoteDatabases(databases)
		}
	}()
}

func MonitorAndDemoteDatabases(databases []Database) {
	utils.Logger.Debug("Checking databases for inactivity.", zap.Int("#databases", len(databases)))

	for _, database := range databases {
		// NOTE: database is already on furthest stage, no demoting possible
		if database.GetStage() >= 3 {
			continue
		}

		database.GetMutex().RLock()

		timeSinceAccess := time.Since(database.GetLastAccessed())
		timeoutDuration := time.Duration(utils.Config.Settings.StageTimeoutSeconds) * time.Second
		shouldDemote := timeSinceAccess >= timeoutDuration

		database.GetMutex().RUnlock()

		if shouldDemote {
			utils.Logger.Debug(
				fmt.Sprintf("Stage Monitoring - Database '%s' inactive for %v, demoting.", database.GetName(), timeSinceAccess),
				zap.Uint("currentStage", database.GetStage()),
				zap.Duration("inactiveDuration", timeSinceAccess),
			)
			go demoteToFartherStage(database)
		}
	}
}

package stages

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	"persisto/src/utils"

	"go.uber.org/zap"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

type Database = interface {
	GetPath() string
	SetPath(path string)
	GetName() string
	GetStage() uint
	SetStage(stage uint)
	GetConnectionString() (string, error)
	GetLastAccessed() time.Time
	SetLastAccessed(time.Time)
	GetRequestCount() uint
	SetRequestCount(uint)
	GetMutex() *sync.RWMutex
}

type Stage struct {
	Index uint
	Name  string
}

var (
	Stages []Stage

	setupStageOnce sync.Once
)

func SetupStages() {
	setupStageOnce.Do(func() {
		utils.Logger.Info("Setting up stages configuration.")

		Stages = []Stage{
			{Index: utils.Config.Storage.Local.StageNumber, Name: utils.Config.Storage.Local.Name},
			{Index: utils.Config.Storage.Remote.StageNumber, Name: utils.Config.Storage.Remote.Name},
		}

		utils.Logger.Info("Stages configuration loaded.", zap.Int("count", len(Stages)), zap.Reflect("stages", Stages))
	})
}

func MoveToStage(database Database, targetStage uint) error {
	utils.Logger.Debug("Moving database to different stage.", zap.Uint("currentStage", database.GetStage()), zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
	if !utils.IsValidStage(targetStage) {
		minStage, maxStage := utils.GetValidStageRange()
		utils.Logger.Error("Invalid targetStage.", zap.Uint("targetStage", targetStage))
		return fmt.Errorf("invalid stage: %d. Valid stages are %d-%d", targetStage, minStage, maxStage)
	}

	if database.GetStage() == targetStage {
		utils.Logger.Error("Database already at targetStage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
		return nil
	}

	originalStage := database.GetStage()

	// Sync data to target stage
	err := syncToStage(database, targetStage)
	if err != nil {
		utils.Logger.Error("Failed to sync database to target stage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database), zap.Error(err))
		return fmt.Errorf("failed to sync database to target stage: %v", err)
	}

	// Update database stage and path
	database.SetStage(targetStage)
	updateDatabasePath(database, targetStage)

	// Verify database integrity for downward moves (closer to user)
	if targetStage < originalStage {
		connectionString, err := database.GetConnectionString()
		if err != nil {
			database.SetStage(originalStage)
			updateDatabasePath(database, originalStage)
			utils.Logger.Error("Failed to get connection string after move, restoring originalStage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database), zap.Error(err))
			return fmt.Errorf("failed to get connection string after move: %v", err)
		}
		err = utils.VerifyDatabaseIntegrity(connectionString)
		if err != nil {
			// TODO: might rollback syncing and return error ?
			utils.Logger.Warn("Database integrity check failed.", zap.Reflect("database", database), zap.Error(err))
		}
	}

	return nil
}

// NOTE: syncToStage syncs database from current stage to target stage without changing the database's stage
func syncToStage(database Database, targetStage uint) error {
	sourceConnection, err := database.GetConnectionString()
	if err != nil {
		utils.Logger.Error("Failed to get source connection string.", zap.Reflect("database", database), zap.Error(err))
		return fmt.Errorf("failed to get source connection string: %v", err)
	}

	sourceDB, err := sql.Open("sqlite3", sourceConnection)
	if err != nil {
		utils.Logger.Error("Failed to open source database.", zap.Error(err))
		return fmt.Errorf("failed to open source database: %v", err)
	}
	defer sourceDB.Close()

	err = sourceDB.Ping()
	if err != nil {
		utils.Logger.Error("Source database ping failed.", zap.Reflect("database", database), zap.String("connectionString", sourceConnection))
		return fmt.Errorf("source database ping failed: %v", err)
	}

	targetConn, err := GetConnectionStringForStage(database, targetStage)
	if err != nil {
		utils.Logger.Error("Failed to get target connection string.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database), zap.Error(err))
		return fmt.Errorf("failed to get target connection string: %v", err)
	}

	targetDB, err := sql.Open("sqlite3", targetConn)
	if err != nil {
		utils.Logger.Error("Failed to open target database.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
		return fmt.Errorf("failed to open target database: %v", err)
	}
	defer targetDB.Close()

	err = targetDB.Ping()
	if err != nil {
		utils.Logger.Error("Target database ping failed.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
		return fmt.Errorf("target database ping failed: %v", err)
	}

	originalStage := database.GetStage()

	err = copyDataBetweenStages(database, originalStage, targetStage)

	if err != nil {
		utils.Logger.Error("Failed to copy database data.", zap.Uint("sourceStage", originalStage), zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
		return fmt.Errorf("failed to copy database data: %v", err)
	}

	return nil
}

func GetStageName(stageIndex uint) string {
	for _, stage := range Stages {
		if stage.Index == stageIndex {
			return stage.Name
		}
	}
	return "Unknown"
}

func GetConfigDefaultStage() uint {
	return utils.Config.Settings.DefaultDatabaseCreationStage
}

func GetConfigAutoStageMovement() bool {
	return utils.Config.Settings.AutoStageMovement
}

func GetConfigStageTimeout() int {
	return utils.Config.Settings.StageTimeoutSeconds
}

func GetConfigRequestThreshold() uint {
	return utils.Config.Settings.RequestCountThreshold
}

func PromoteToCloserStage(database Database) {
	database.GetMutex().Lock()
	defer database.GetMutex().Unlock()

	if utils.IsClosestStage(database.GetStage()) {
		utils.Logger.Warn("Database already at closest stage, no promotion needed.", zap.Reflect("database", database))
		return
	}

	targetStage := utils.GetNextCloserStage(database.GetStage())
	if targetStage == 0 {
		utils.Logger.Warn("Cannot promote database further, already at closest stage.", zap.Reflect("database", database))
		return
	}
	utils.Logger.Debug(
		"Checking if database should be promoted to closer stage.",
		zap.Reflect("database", database),
		zap.Uint("currentStage", database.GetStage()),
		zap.Uint("targetStage", targetStage),
		zap.Uint("requestCount", database.GetRequestCount()),
	)

	database.SetRequestCount(0)

	sourceConn, err := database.GetConnectionString()
	if err != nil {
		utils.Logger.Error("Failed to get source connection for promotion",
			zap.Reflect("database", database),
			zap.Error(err))
		return
	}

	sourceDB, err := sql.Open("sqlite3", sourceConn)
	if err != nil {
		utils.Logger.Error("Failed to open source database for promotion",
			zap.Reflect("database", database),
			zap.Error(err))
		return
	}

	if err := sourceDB.Ping(); err != nil {
		utils.Logger.Error("Source database not accessible for promotion",
			zap.Reflect("database", database),
			zap.Error(err))
		return
	}

	sourceDB.Close()

	err = MoveToStage(database, targetStage)
	if err != nil {
		utils.Logger.Error(
			"Failed to auto-promote database to closer stage.",
			zap.Reflect("database", database),
			zap.Uint("targetStage", targetStage),
			zap.Error(err),
		)
	} else {
		utils.Logger.Info("Successfully promoted database to closer stage.",
			zap.Reflect("database", database),
			zap.Uint("targetStage", targetStage),
		)
	}
}

func demoteToFartherStage(database Database) {
	database.GetMutex().Lock()
	defer database.GetMutex().Unlock()

	if utils.IsFarthestStage(database.GetStage()) {
		utils.Logger.Warn(
			"Database already at farthest stage, no demotion needed.",
			zap.Reflect("database", database),
		)
		return
	}

	timeSinceAccess := time.Since(database.GetLastAccessed())
	timeoutDuration := time.Duration(utils.Config.Settings.StageTimeoutSeconds) * time.Second

	if timeSinceAccess < timeoutDuration {
		utils.Logger.Debug(
			"Database not ready for demotion due to recent access.",
			zap.Reflect("database", database),
			zap.Duration("timeSinceAccess", timeSinceAccess),
			zap.Duration("timeoutDuration", timeoutDuration),
		)
		return
	}

	targetStage := utils.GetNextFartherStage(database.GetStage())
	if targetStage == 0 {
		utils.Logger.Warn("Cannot demote database further, already at farthest stage.", zap.Reflect("database", database))
		return
	}
	utils.Logger.Info(
		"Auto-demoting database to farther stage due to inactivity.",
		zap.Reflect("database", database),
		zap.Uint("currentStage", database.GetStage()),
		zap.Uint("targetStage", targetStage),
		zap.Duration("timeSinceAccess", timeSinceAccess),
	)

	if utils.Config.Settings.AutoSyncEnabled && !utils.IsFarthestStage(database.GetStage()) {
		utils.Logger.Debug(
			"Syncing database to upper stages before demotion.",
			zap.Reflect("database", database),
		)
		// TODO: i think we should sync only to one stage up and not loop over everything
		for stage := utils.GetNextFartherStage(database.GetStage()); stage != 0 && stage <= utils.GetFarthestStage(); stage = utils.GetNextFartherStage(stage) {
			err := syncToStage(database, stage)
			if err != nil {
				utils.Logger.Error(
					"Failed to sync database to upper stage before demotion.",
					zap.Reflect("database", database),
					zap.Uint("stage", stage),
					zap.Error(err),
				)
				// TODO: is this the right behavior?
				// NOTE: we continue with demotion even if sync fails, but log the error
			} else {
				err = verifyDatabaseAtStage(database, stage)
				if err != nil {
					// TODO: we should handle this error properly here and maybe rollback the sync
					utils.Logger.Warn(
						"Database verification failed after sync to upper stage.",
						zap.Reflect("database", database),
						zap.Uint("stage", stage),
						zap.Error(err),
					)
				} else {
					utils.Logger.Debug(
						"Database successfully verified at upper stage.",
						zap.Reflect("database", database),
						zap.Uint("stage", stage),
					)
				}
			}
		}
		utils.Logger.Debug("Pre-demotion sync completed.", zap.Reflect("database", database))
	}

	database.SetRequestCount(0)

	err := MoveToStage(database, targetStage)

	if err != nil {
		utils.Logger.Error(
			"Failed to auto-demote database to farther stage.",
			zap.Reflect("database", database),
			zap.Uint("targetStage", targetStage),
			zap.Error(err),
		)
	}
}

func SyncToUpperStages(database Database) {
	if !utils.Config.Settings.AutoSyncEnabled {
		return
	}

	// NOTE: prevent concurrent sync operations on the same database
	database.GetMutex().Lock()
	defer database.GetMutex().Unlock()

	utils.Logger.Debug("Syncing database to upper stages.", zap.Reflect("database", database), zap.Uint("currentStage", database.GetStage()))

	// TODO: rather than syncing to all existing upper stages, we should sync up to the next persistent stage and stop
	// NOTE: sync to each upper stages
	for stage := utils.GetNextFartherStage(database.GetStage()); stage != 0 && stage <= utils.GetFarthestStage(); stage = utils.GetNextFartherStage(stage) {
		err := syncToStage(database, stage)
		if err != nil {
			utils.Logger.Error(
				"Failed to sync database to upper stage.",
				zap.Reflect("database", database),
				zap.Uint("stage", stage),
				zap.Error(err),
			)
			break
		}
	}

	utils.Logger.Debug("Sync completed for database.", zap.Reflect("database", database), zap.Uint("currentStage", database.GetStage()))
}

func updateDatabasePath(database Database, targetStage uint) {
	name := database.GetName()

	switch targetStage {
	case utils.GetLocalStage():
		database.SetPath(fmt.Sprintf("%s/%s.db", utils.Config.Storage.Local.DirectoryPath, name))
	case utils.GetRemoteStage():
		database.SetPath(name)
	}
}

func verifyDatabaseAtStage(database Database, stage uint) error {
	// NOTE: temporarily set stage to target one (where the database have supposedly been copied to) check the connection.
	originalStage := database.GetStage()
	database.SetStage(stage)
	defer database.SetStage(originalStage)

	connStr, err := GetConnectionStringForStage(database, stage)
	if err != nil {
		return fmt.Errorf("failed to get connection string for stage %d: %v", stage, err)
	}

	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return fmt.Errorf("failed to open database at stage %d: %v", stage, err)
	}
	defer db.Close()

	err = db.Ping()
	if err != nil {
		return fmt.Errorf("failed to ping database at stage %d: %v", stage, err)
	}

	var tableCount int
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tableCount)
	if err != nil {
		return fmt.Errorf("failed to count tables at stage %d: %v", stage, err)
	}

	if tableCount == 0 {
		return fmt.Errorf("database at stage %d exists but has no tables (possible data loss)", stage)
	}

	utils.Logger.Debug(
		"Database verification successful",
		zap.Uint("stage", stage),
		zap.Int("tableCount", tableCount),
		zap.Reflect("database", database),
	)
	return nil
}

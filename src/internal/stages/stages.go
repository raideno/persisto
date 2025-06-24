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
			{Index: utils.Config.Storage.Memory.StageNumber, Name: utils.Config.Storage.Memory.Name},
			{Index: utils.Config.Storage.Local.StageNumber, Name: utils.Config.Storage.Local.Name},
			{Index: utils.Config.Storage.Remote.StageNumber, Name: utils.Config.Storage.Remote.Name},
		}

		utils.Logger.Info("Stages configuration loaded.", zap.Int("count", len(Stages)), zap.Reflect("stages", Stages))
	})
}

func MoveToStage(database Database, targetStage uint) error {
	utils.Logger.Debug("Moving database to different stage.", zap.Uint("currentStage", database.GetStage()), zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
	if targetStage < 1 || targetStage > 3 {
		utils.Logger.Error("Invalid targetStage.", zap.Uint("targetStage", targetStage))
		return fmt.Errorf("invalid stage: %d. Valid stages are 1-3", targetStage)
	}

	if database.GetStage() == targetStage {
		utils.Logger.Error("Database already at targetStage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
		return nil
	}

	if targetStage < database.GetStage() {
		return syncDown(database, targetStage)
	} else {
		return syncUp(database, targetStage)
	}
}

func syncDown(database Database, targetStage uint) error {
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

	originalStage := database.GetStage()
	database.SetStage(targetStage)

	targetConn, err := GetConnectionStringForStage(database, targetStage)
	if err != nil {
		database.SetStage(originalStage)
		utils.Logger.Error("Failed to get target connection string, restoring originalStage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database), zap.Error(err))
		return fmt.Errorf("failed to get target connection string: %v", err)
	}

	targetDB, err := sql.Open("sqlite3", targetConn)
	if err != nil {
		database.SetStage(originalStage)
		utils.Logger.Error("Failed to open target database, restoring originalStage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
		return fmt.Errorf("failed to open target database: %v", err)
	}
	defer targetDB.Close()

	err = targetDB.Ping()
	if err != nil {
		database.SetStage(originalStage)
		utils.Logger.Error("Target database ping failed, restoring originalStage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
		return fmt.Errorf("target database ping failed: %v", err)
	}

	err = copyDatabaseData(database, originalStage, targetStage)
	if err != nil {
		database.SetStage(originalStage)
		utils.Logger.Error("Target database copy failed, restoring originalStage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database))
		return fmt.Errorf("failed to copy database data: %v", err)
	}

	// Update database path after successful copy
	updateDatabasePath(database, targetStage)

	connectionString, err := database.GetConnectionString()
	if err != nil {
		database.SetStage(originalStage)
		utils.Logger.Error("Failed to get connection string after copy, restoring originalStage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database), zap.Error(err))
		return fmt.Errorf("failed to get connection string after copy: %v", err)
	}
	err = utils.VerifyDatabaseIntegrity(connectionString)
	if err != nil {
		// TODO: might rollback syncing and return error ?
		utils.Logger.Warn("Database integrity check failed.", zap.Reflect("database", database), zap.Error(err))
	}

	return nil
}

// NOTE: syncUp syncs database from higher stage to lower stage (bringing data closer to user)
func syncUp(database Database, targetStage uint) error {
	sourceConnection, err := database.GetConnectionString()
	if err != nil {
		utils.Logger.Error("Failed to get source connection string.", zap.Reflect("database", database), zap.Error(err))
		return fmt.Errorf("failed to get source connection string: %v", err)
	}

	sourceDB, err := sql.Open("sqlite3", sourceConnection)
	if err != nil {
		return fmt.Errorf("failed to open source database: %v", err)
	}
	defer sourceDB.Close()

	originalStage := database.GetStage()
	database.SetStage(targetStage)

	targetConn, err := GetConnectionStringForStage(database, targetStage)
	if err != nil {
		database.SetStage(originalStage)
		utils.Logger.Error("Failed to get target connection string, restoring originalStage.", zap.Uint("targetStage", targetStage), zap.Reflect("database", database), zap.Error(err))
		return fmt.Errorf("failed to get target connection string: %v", err)
	}

	targetDB, err := sql.Open("sqlite3", targetConn)
	if err != nil {
		// NOTE: restore original stage on error
		database.SetStage(originalStage)
		return fmt.Errorf("failed to open target database: %v", err)
	}
	defer targetDB.Close()

	err = copyDatabaseData(database, originalStage, targetStage)
	if err != nil {
		// NOTE: restore original stage on error
		database.SetStage(originalStage)
		return fmt.Errorf("failed to copy database data: %v", err)
	}

	updateDatabasePath(database, targetStage)

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

// NOTE: ensures the database is available on all stages from 1 to current stage
func SyncToAllLowerStages(database Database) error {
	utils.Logger.Debug("Syncing database to all lower stages.", zap.Reflect("database", database), zap.Uint("currentStage", database.GetStage()))

	originalStage := database.GetStage()

	for stage := database.GetStage() - 1; stage >= 1; stage-- {
		err := syncDown(database, stage)
		if err != nil {
			return fmt.Errorf("failed to sync to stage %d: %v", stage, err)
		}
		// restore original stage after each sync
		database.SetStage(originalStage)
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

	if database.GetStage() <= 1 {
		utils.Logger.Warn("Database already at closest stage, no promotion needed.", zap.Reflect("database", database))
		return
	}

	targetStage := database.GetStage() - 1
	utils.Logger.Debug("Checking if database should be promoted to closer stage.",
		zap.Reflect("database", database),
		zap.Uint("currentStage", database.GetStage()),
		zap.Uint("targetStage", targetStage),
		zap.Uint("requestCount", database.GetRequestCount()),
	)

	database.SetRequestCount(0)

	err := MoveToStage(database, targetStage)
	if err != nil {
		utils.Logger.Error(
			"Failed to auto-promote database to closer stage.",
			zap.Reflect("database", database),
			zap.Uint("targetStage", targetStage),
			zap.Error(err),
		)
	}
}

func demoteToFartherStage(database Database) {
	database.GetMutex().Lock()
	defer database.GetMutex().Unlock()

	if database.GetStage() >= 3 {
		utils.Logger.Warn("Database already at farthest stage, no demotion needed.", zap.Reflect("database", database))
		return
	}

	timeSinceAccess := time.Since(database.GetLastAccessed())
	timeoutDuration := time.Duration(utils.Config.Settings.StageTimeoutSeconds) * time.Second

	if timeSinceAccess < timeoutDuration {
		utils.Logger.Debug("Database not ready for demotion due to recent access.",
			zap.Reflect("database", database),
			zap.Duration("timeSinceAccess", timeSinceAccess),
			zap.Duration("timeoutDuration", timeoutDuration),
		)
		return
	}

	targetStage := database.GetStage() + 1
	utils.Logger.Info("Auto-demoting database to farther stage due to inactivity.",
		zap.Reflect("database", database),
		zap.Uint("currentStage", database.GetStage()),
		zap.Uint("targetStage", targetStage),
		zap.Duration("timeSinceAccess", timeSinceAccess),
	)

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

	// NOTE: i use mutex to prevent concurrent sync operations on the same database
	database.GetMutex().Lock()
	defer database.GetMutex().Unlock()

	utils.Logger.Debug("Syncing database to upper stages.", zap.Reflect("database", database), zap.Uint("currentStage", database.GetStage()))

	originalStage := database.GetStage()

	// NOTE: sync to each upper stage
	for stage := database.GetStage() + 1; stage <= 3; stage++ {
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
		// NOTE: ensure database remains at original stage
		database.SetStage(originalStage)
		updateDatabasePath(database, originalStage)
	}

	utils.Logger.Debug("Sync completed for database.", zap.Reflect("database", database), zap.Uint("currentStage", database.GetStage()))
}

func updateDatabasePath(database Database, targetStage uint) {
	name := database.GetName()

	switch targetStage {
	case utils.Config.Storage.Memory.StageNumber:
		database.SetPath(fmt.Sprintf("/%s", name))
	case utils.Config.Storage.Local.StageNumber:
		database.SetPath(fmt.Sprintf("./storage/%s.db", name))
	case utils.Config.Storage.Remote.StageNumber:
		database.SetPath(name)
	}
}

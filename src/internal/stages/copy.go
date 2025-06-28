package stages

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"persisto/src/utils"
	"persisto/src/vfs/localvfs"
	"persisto/src/vfs/remotevfs"

	"go.uber.org/zap"
)

func copyDataBetweenStages(database Database, sourceStage, targetStage uint) error {
	utils.Logger.Debug(
		"Starting copy between stages",
		zap.Uint("sourceStage", sourceStage),
		zap.Uint("targetStage", targetStage),
		zap.Reflect("database", database),
	)

	sourceConnection, err := GetConnectionStringForStage(database, sourceStage)
	if err != nil {
		utils.Logger.Error("Failed to get source connection string.", zap.Error(err))
		return fmt.Errorf("failed to get source connection string: %v", err)
	}

	targetConnection, err := GetConnectionStringForStage(database, targetStage)
	if err != nil {
		utils.Logger.Error("Failed to get target connection string.", zap.Error(err))
		return fmt.Errorf("failed to get target connection string: %v", err)
	}

	err = deleteTargetFile(database.GetName(), targetStage)
	if err != nil {
		utils.Logger.Warn("Failed to delete existing target file", zap.Error(err))
	}

	sourceDB, err := sql.Open("sqlite3", sourceConnection)
	if err != nil {
		utils.Logger.Error("Failed to open source database.", zap.Error(err))
		return fmt.Errorf("failed to open source database: %v", err)
	}
	defer sourceDB.Close()

	if err := sourceDB.Ping(); err != nil {
		utils.Logger.Error("Failed to ping source database.", zap.Error(err))
		return fmt.Errorf("failed to ping source database: %v", err)
	}

	return executeDatabaseCopy(sourceDB, targetConnection)
}

func executeDatabaseCopy(sourceDB *sql.DB, targetConnection string) error {
	utils.Logger.Debug("Executing database copy", zap.String("targetConnection", targetConnection))

	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		_, lastErr = sourceDB.Exec("VACUUM INTO ?", targetConnection)
		if lastErr == nil {
			utils.Logger.Debug("Successfully executed database copy", zap.String("targetConnection", targetConnection))
			return nil
		}

		if strings.Contains(lastErr.Error(), "output file already exists") && attempt < maxRetries-1 {
			utils.Logger.Warn("VACUUM INTO failed due to existing file, retrying after deletion",
				zap.Int("attempt", attempt+1),
				zap.String("targetConnection", targetConnection),
				zap.Error(lastErr))

			time.Sleep(100 * time.Millisecond)
			continue
		}

		utils.Logger.Error("VACUUM INTO failed", zap.Error(lastErr), zap.String("targetConnection", targetConnection))
		break
	}

	return fmt.Errorf("failed to copy database: %v", lastErr)
}

func GetConnectionStringForStage(database Database, stage uint) (string, error) {
	name := database.GetName()

	switch stage {
	case utils.GetLocalStage():
		localPath := fmt.Sprintf("%s/%s.db", utils.Config.Storage.Local.DirectoryPath, name)
		return fmt.Sprintf("file:%s?vfs=disk", localPath), nil
	case utils.GetRemoteStage():
		dbName := name
		if !strings.HasSuffix(dbName, ".db") {
			dbName += ".db"
		}
		return fmt.Sprintf("file:%s?vfs=r2", dbName), nil
	default:
		return "", fmt.Errorf("invalid stage: %d", stage)
	}
}

func deleteTargetFile(name string, targetStage uint) error {
	utils.Logger.Debug("Deleting target file if exists",
		zap.String("name", name),
		zap.Uint("targetStage", targetStage))

	switch targetStage {
	case utils.GetLocalStage():
		localPath := fmt.Sprintf("%s/%s.db", utils.Config.Storage.Local.DirectoryPath, name)
		err := localvfs.Delete(localPath)
		if err != nil {
			utils.Logger.Debug("Failed to delete local file (may not exist)",
				zap.String("localPath", localPath),
				zap.Error(err))
		}
		return nil

	case utils.GetRemoteStage():
		remoteName := name
		if !strings.HasSuffix(remoteName, ".db") {
			remoteName += ".db"
		}
		err := remotevfs.Delete(remoteName)
		if err != nil {
			utils.Logger.Debug("Failed to delete remote file (may not exist)",
				zap.String("remoteName", remoteName),
				zap.Error(err))
		}
		return nil

	default:
		return fmt.Errorf("unsupported stage for deletion: %d", targetStage)
	}
}

package stages

import (
	"fmt"
	"strings"

	"persisto/src/utils"
	"persisto/src/vfs/localvfs"
	"persisto/src/vfs/remotevfs"

	"go.uber.org/zap"
)

func RemoveFromStage(database Database, stage uint) error {
	utils.Logger.Debug(
		"Removing database from stage.",
		zap.Reflect("database", database),
		zap.Uint("stage", stage),
	)

	if !utils.IsRemovableStage(stage) {
		removableStages := utils.GetRemovableStages()
		utils.Logger.Error(
			"Invalid stage for removal.",
			zap.Uint("stage", stage),
			zap.Reflect("database", database),
		)
		return fmt.Errorf("invalid stage: %d. Valid removable stages are %v", stage, removableStages)
	}

	if stage == database.GetStage() {
		utils.Logger.Error(
			"Cannot remove database from its current active stage.",
			zap.Uint("stage", stage),
			zap.Reflect("database", database),
		)
		return fmt.Errorf("cannot remove database from its current active stage %d", stage)
	}

	switch stage {
	case utils.GetLocalStage():
		return removeFromLocalStage(database)
	case utils.GetRemoteStage():
		return removeFromR2Stage(database)
	}

	utils.Logger.Error("Invalid stage for removal.", zap.Uint("stage", stage), zap.Reflect("database", database))

	return nil
}

func removeFromLocalStage(database Database) error {
	err := localvfs.Delete(database.GetPath())

	if err != nil {
		utils.Logger.Error(
			"Failed to remove local file.",
			zap.Error(err),
			zap.String("path", database.GetPath()),
			zap.Reflect("database", database),
		)
		return fmt.Errorf("failed to remove local file: %v", err)
	}

	utils.Logger.Debug("Successfully removed database from local disk.", zap.String("path", database.GetPath()), zap.Reflect("database", database))

	return nil
}

func removeFromR2Stage(database Database) error {
	r2Key := database.GetName()
	if !strings.HasSuffix(r2Key, ".db") {
		r2Key += ".db"
	}

	err := remotevfs.Delete(r2Key)
	if err != nil {
		utils.Logger.Error(
			"Failed to delete database from R2 storage.",
			zap.Error(err),
			zap.String("r2Key", r2Key),
			zap.Reflect("database", database),
		)
		return fmt.Errorf("failed to delete database from R2: %v", err)
	}

	utils.Logger.Debug(
		"Successfully deleted database from R2 storage.",
		zap.String("r2Key", r2Key),
		zap.Reflect("database", database),
	)

	return nil
}

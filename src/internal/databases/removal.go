package databases

import (
	"fmt"

	"persisto/src/utils"

	"go.uber.org/zap"
)

func (database *Database) removeFromDatabasesList() error {
	if Dbs == nil {
		return fmt.Errorf("databases list is not initialized")
	}

	for i, db := range Dbs.Items {
		if db.Name == database.Name {
			Dbs.Items = append(Dbs.Items[:i], Dbs.Items[i+1:]...)
			utils.Logger.Info(
				"Successfully removed database from list",
				zap.String("database", database.Name),
				zap.Int("remainingDatabases", len(Dbs.Items)),
			)
			return nil
		}
	}

	utils.Logger.Warn(
		"Database not found in list during deletion",
		zap.String("database", database.Name),
	)

	return nil
}

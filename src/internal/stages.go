package internal

import (
	"sync"

	"persisto/src/internal/databases"
	"persisto/src/internal/stages"
)

var (
	stagesMonitoringSetupOnce sync.Once
)

func SetupStagesMonitoring() {
	stagesMonitoringSetupOnce.Do(func() {
		getDatabases := func() []stages.Database {
			if databases.Dbs == nil {
				return []stages.Database{}
			}

			result := make([]stages.Database, len(databases.Dbs.Items))
			for i, database := range databases.Dbs.Items {
				result[i] = database
			}
			return result
		}

		stages.SetupStageMonitor(getDatabases)
	})
}

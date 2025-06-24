package databases

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"persisto/src/internal/stages"
	"persisto/src/utils"
	"persisto/src/vfs/localvfs"
	"persisto/src/vfs/memoryvfs"
	"persisto/src/vfs/remotevfs"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"go.uber.org/zap"
)

var (
	DEFAULT_DATABASE_PATH string = "./storage"
)

type Database struct {
	Path         string
	Name         string
	Stage        uint
	LastAccessed time.Time
	RequestCount uint

	mutex sync.RWMutex
}

type Databases struct {
	Items []*Database
}

var (
	Dbs                *Databases
	DatabaseSetupError error

	databasesSetupOnce sync.Once
)

func SetupDatabases() (*Databases, error) {
	databasesSetupOnce.Do(func() {
		utils.Logger.Info("Setting up databases.")

		databases, err := ListDatabases(utils.Config.Storage.Remote.StageNumber)

		if err != nil {
			utils.Logger.Error("Failed to prefetch databases.", zap.Error(err))
			DatabaseSetupError = err
			return
		}

		Dbs = databases

		utils.Logger.Info("Successfully setup databases.", zap.Reflect("databases", databases))
	})

	return Dbs, DatabaseSetupError
}

func (database *Database) GetConnectionString() (string, error) {
	switch database.Stage {
	case utils.Config.Storage.Memory.StageNumber:
		return fmt.Sprintf("file:/%s?vfs=memory", database.Name), nil
	case utils.Config.Storage.Local.StageNumber:
		return fmt.Sprintf("file:%s?vfs=disk", database.Path), nil
	case utils.Config.Storage.Remote.StageNumber:
		dbName := database.Name
		if !strings.HasSuffix(dbName, ".db") {
			dbName += ".db"
		}
		return fmt.Sprintf("file:%s?vfs=r2", dbName), nil
	default:
		utils.Logger.Error("Invalid database stage provided.", zap.Uint("stage", database.Stage))
		return fmt.Sprintf("file:%s?vfs=disk", database.Path), nil
	}
}

func (databases *Databases) FindByName(name string) (*Database, error) {
	for i := range databases.Items {
		if databases.Items[i].Name == name {
			return databases.Items[i], nil
		}
	}
	return nil, fmt.Errorf("Database not found")
}

func (databases *Databases) CreateDatabaseAndInitialize(name string, stage uint) (*Database, error) {
	var path string
	
	switch stage {
	case utils.Config.Storage.Memory.StageNumber:
		path = fmt.Sprintf("/%s", name)
	case utils.Config.Storage.Local.StageNumber:
		path = fmt.Sprintf("%s/%s.db", DEFAULT_DATABASE_PATH, name)
	case utils.Config.Storage.Remote.StageNumber:
		path = name
	default:
		utils.Logger.Error("Invalid stage provided for database creation.", zap.Uint("stage", stage))
		return nil, fmt.Errorf("invalid stage: %d. Valid stages are 1-3", stage)
	}

	database := &Database{
		Path:         path,
		Name:         name,
		Stage:        stage,
		LastAccessed: time.Now(),
		RequestCount: 0,
	}

	err := database.initialize()
	if err != nil {
		utils.Logger.Error("Failed to initialize database.", zap.Reflect("database", database), zap.Error(err))
		return nil, err
	}

	databases.Items = append(databases.Items, database)

	return database, nil
}

func (database *Database) initialize() error {
	connectionString, err := database.GetConnectionString()
	if err != nil {
		utils.Logger.Error("Failed to get connection string for database initialization.", zap.Error(err), zap.Reflect("database", database))
		return err
	}

	connection, err := sql.Open("sqlite3", connectionString)
	if err != nil {
		utils.Logger.Error("Error creating database connection", zap.String("connectionString", connectionString), zap.String("name", database.Name), zap.Error(err))
		return err
	}
	defer connection.Close()

	err = connection.Ping()
	if err != nil {
		utils.Logger.Error("Database initialization failed - ping failed", zap.String("connectionString", connectionString), zap.String("name", database.Name), zap.Error(err))
		return err
	}

	// TODO: replace hack with a more general approach that creates a file in the appropriate stage
	// NOTE: for remote databases, we need to ensure the file is actually created in the storage, SQLite won't create the file until we perform an operation that requires writing
	if database.Stage == utils.Config.Storage.Remote.StageNumber {
		utils.Logger.Debug("Creating database file in remote storage", zap.String("name", database.Name))
		
		// NOTE: create the database file by performing a write operation
		_, err = connection.Exec("CREATE TABLE IF NOT EXISTS _persisto_init (id INTEGER PRIMARY KEY)")
		if err != nil {
			utils.Logger.Error("Database initialization failed - failed to create init table in remote storage", zap.String("connectionString", connectionString), zap.String("name", database.Name), zap.Error(err))
			return err
		}
		
		// NOTE: clean up the init table - this ensures the file exists and is properly initialized
		_, err = connection.Exec("DROP TABLE IF EXISTS _persisto_init")
		if err != nil {
			utils.Logger.Error("Database initialization failed - failed to cleanup init table in remote storage", zap.String("connectionString", connectionString), zap.String("name", database.Name), zap.Error(err))
			return err
		}
		
		utils.Logger.Debug("Successfully created database file in remote storage", zap.String("name", database.Name))
	} else {
		// NOTE: for non-remote databases, just test with a simple query
		_, err = connection.Exec("SELECT 1")
		if err != nil {
			utils.Logger.Error("Database initialization failed - test query failed", zap.String("connectionString", connectionString), zap.String("name", database.Name), zap.Error(err))
			return err
		}
	}

	utils.Logger.Info("Database successfully initialized", zap.String("name", database.Name), zap.Uint("stage", database.Stage), zap.String("connectionString", connectionString))
	
	return nil
}

func (database *Database) Query(query string) (utils.QueryResultType, error) {
	utils.Logger.Debug("Database before request handling.", zap.Reflect("database", database))

	err := database.handleAccess()
	if err != nil {
		utils.Logger.Warn("Failed to handle database request.", zap.Error(err))
	}

	connectionString, err := database.GetConnectionString()
	if err != nil {
		utils.Logger.Error("Failed to get connection string for database.", zap.Error(err), zap.Reflect("database", database))
		return utils.QueryResultType{}, err
	}

	utils.Logger.Debug("Database after request handling.", zap.Reflect("database", database), zap.Reflect("connectionString", connectionString))

	connection, err := sql.Open("sqlite3", connectionString)
	if err != nil {
		return utils.QueryResultType{}, err
	}
	defer connection.Close()

	err = connection.Ping()
	if err != nil {
		utils.Logger.Error("Database PING failed for connection.", zap.Error(err))
		return utils.QueryResultType{}, err
	}
	utils.Logger.Debug("Database PING was successful.")

	rows, err := connection.Query(query)
	if err != nil {
		utils.Logger.Error("Query failed.", zap.String("query", query), zap.Reflect("database", database))
		return utils.QueryResultType{}, err
	}

	output, err := utils.QueryResultToMaps(rows)

	if utils.Config.Settings.AutoStageMovement && database.RequestCount >= utils.Config.Settings.RequestCountThreshold {
		utils.Logger.Info("Database stage promotion.", zap.Reflect("database", database))
		go stages.PromoteToCloserStage(database)
	}

	return output, err
}

func (database *Database) Execute(query string) (utils.ExecResultType, error) {
	utils.Logger.Debug("Database before request handling.", zap.Reflect("database", database))

	err := database.handleAccess()
	if err != nil {
		utils.Logger.Warn("Failed to handle database request.", zap.Error(err))
	}

	connectionString, err := database.GetConnectionString()
	if err != nil {
		utils.Logger.Error("Failed to get connection string for database.", zap.Error(err), zap.Reflect("database", database))
		return utils.ExecResultType{}, err
	}

	utils.Logger.Debug("Database after request handling.", zap.Reflect("database", database), zap.Reflect("connectionString", connectionString))

	connection, err := sql.Open("sqlite3", connectionString)
	if err != nil {
		return utils.ExecResultType{}, err
	}
	defer connection.Close()

	result, err := connection.Exec(query)
	if err != nil {
		return utils.ExecResultType{}, err
	}

	output, err := utils.ExecResultToMap(result)

	if utils.Config.Settings.AutoStageMovement && database.RequestCount >= utils.Config.Settings.RequestCountThreshold {
		utils.Logger.Info("Database stage promotion.", zap.Reflect("database", database))
		go stages.PromoteToCloserStage(database)
	}

	// NOTE: trigger sync to upper stages after write operations
	if utils.Config.Settings.AutoSyncEnabled && utils.IsWriteOperation(query) {
		go stages.SyncToUpperStages(database)
	}

	return output, err
}

func (database *Database) Delete() error {
	utils.Logger.Info(
		"Starting database deletion process",
		zap.String("database", database.Name),
		zap.Uint("currentStage", database.Stage),
	)

	database.mutex.Lock()
	defer database.mutex.Unlock()

	persistentStage := utils.Config.Settings.PersistenceStage

	// TODO: verify that databases are being synced before being deleted
	for stage := persistentStage; stage >= database.Stage; stage-- {
		err := stages.RemoveFromStage(database, stage)
		if err != nil {
			utils.Logger.Error(
				"Failed to remove database from stage",
				zap.String("database", database.Name),
				zap.Uint("stage", stage),
				zap.Error(err),
			)
		} else {
			utils.Logger.Info(
				"Successfully removed database from stage",
				zap.String("database", database.Name),
				zap.Uint("stage", stage),
			)
		}
	}

	// TODO: what if all removals fail ?
	err := database.removeFromDatabasesList()
	if err != nil {
		utils.Logger.Error(
			"Failed to remove database from in-memory list",
			zap.String("database", database.Name),
			zap.Error(err),
		)
		return fmt.Errorf("failed to remove database from in-memory list: %v", err)
	}

	utils.Logger.Info("Database deletion completed successfully", zap.String("database", database.Name))
	return nil
}

func (database *Database) handleAccess() error {
	database.mutex.Lock()
	defer database.mutex.Unlock()

	prevCount := database.RequestCount
	database.LastAccessed = time.Now()
	database.RequestCount++

	utils.Logger.Debug("Handling database request",
		zap.String("database", database.Name),
		zap.Uint("previousCount", prevCount),
		zap.Uint("currentCount", database.RequestCount),
		zap.Time("lastAccessed", database.LastAccessed),
	)

	return nil
}

func ListDatabases(stageIndex uint) (*Databases, error) {
	var databases []*Database

	switch stageIndex {
	case utils.Config.Storage.Memory.StageNumber:
		memoryDatabases := memoryvfs.ListDatabases()
		for _, memDb := range memoryDatabases {
			databases = append(databases, &Database{
				// NOTE: memory databases use a path with a leading slash
				Path:         fmt.Sprintf("/%s", memDb.Name),
				Name:         memDb.Name,
				Stage:        utils.Config.Storage.Memory.StageNumber,
				LastAccessed: time.Now(),
				RequestCount: 0,
			})
		}

	case utils.Config.Storage.Local.StageNumber:
		files, err := localvfs.ListFiles(DEFAULT_DATABASE_PATH)
		if err != nil {
			return nil, err
		}

		for _, file := range files {
			if file.IsDir {
				continue
			}
			if strings.HasSuffix(file.Name, ".db") {
				baseName := strings.TrimSuffix(file.Name, ".db")

				databases = append(databases, &Database{
					Path:         file.FullPath,
					Name:         baseName,
					Stage:        utils.Config.Storage.Local.StageNumber,
					LastAccessed: time.Now(),
					RequestCount: 0,
				})
			}
		}

	case utils.Config.Storage.Remote.StageNumber:
		r2Databases, err := remotevfs.ListDatabases()
		if err != nil {
			utils.Logger.Error("Failed to list R2 databases.", zap.Error(err))
			return nil, fmt.Errorf("invalid stage index= %d. Valid stages are 1-3", stageIndex)
		} else {
			for _, r2Db := range r2Databases {
				databases = append(databases, &Database{
					Path:         r2Db.Path,
					Name:         r2Db.Name,
					Stage:        r2Db.Stage,
					LastAccessed: r2Db.LastAccessed,
					RequestCount: r2Db.RequestCount,
				})
			}
		}

	default:
		utils.Logger.Error("Invalid stage index provided.", zap.Uint("stageIndex", stageIndex))
		return nil, fmt.Errorf("invalid stage index: %d. Valid stages are 1-3", stageIndex)
	}

	utils.Logger.Debug(
		fmt.Sprintf("Found %d databases at stage %d (%s)", len(databases), stageIndex, stages.GetStageName(stageIndex)),
		zap.Reflect("databases", databases),
	)

	return &Databases{Items: databases}, nil
}

func (database *Database) GetPath() string {
	return database.Path
}

func (database *Database) SetPath(path string) {
	database.Path = path
}

func (database *Database) GetName() string {
	return database.Name
}

func (database *Database) GetStage() uint {
	return database.Stage
}

func (database *Database) SetStage(stage uint) {
	database.Stage = stage
}

func (database *Database) GetLastAccessed() time.Time {
	return database.LastAccessed
}

func (database *Database) SetLastAccessed(t time.Time) {
	database.LastAccessed = t
}

func (database *Database) GetRequestCount() uint {
	return database.RequestCount
}

func (database *Database) SetRequestCount(count uint) {
	database.RequestCount = count
}

func (database *Database) GetMutex() *sync.RWMutex {
	return &database.mutex
}

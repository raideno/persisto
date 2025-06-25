package stages

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"persisto/src/utils"

	"go.uber.org/zap"
)

func copyDatabaseData(database Database, sourceStage, targetStage uint) error {
	utils.Logger.Info(
		"Copying database file.",
		zap.Reflect("database", database),
		zap.Uint("sourceStage", sourceStage),
		zap.Uint("targetStage", targetStage),
	)

	return copyDataBetweenStages(database, sourceStage, targetStage)
}

func copyDataBetweenStages(database Database, sourceStage, targetStage uint) error {
	sourceConn, err := GetConnectionStringForStage(database, sourceStage)
	if err != nil {
		utils.Logger.Error("Failed to get source connection string.", zap.Error(err))
		return fmt.Errorf("failed to get source connection string: %v", err)
	}

	targetConn, err := GetConnectionStringForStage(database, targetStage)
	if err != nil {
		utils.Logger.Error("Failed to get target connection string.", zap.Error(err))
		return fmt.Errorf("failed to get target connection string: %v", err)
	}

	sourceDB, err := sql.Open("sqlite3", sourceConn)
	if err != nil {
		utils.Logger.Error("Failed to open source database.", zap.Error(err))
		return fmt.Errorf("failed to open source database: %v", err)
	}
	defer sourceDB.Close()

	if sourceStage == utils.Config.Storage.Memory.StageNumber {
		return copyFromMemoryStage(sourceDB, targetConn)
	}

	// For cross-VFS copying, we need to use a different approach
	// VACUUM INTO doesn't work well across different VFS systems
	if sourceStage != targetStage {
		return copyAcrossVFS(sourceDB, targetConn)
	}

	vacuumQuery := "VACUUM INTO ?"
	_, err = sourceDB.Exec(vacuumQuery, getTargetPath(database.GetName(), targetStage))
	if err != nil {
		utils.Logger.Error("Failed to vacuum database to target.", zap.Error(err))
		return fmt.Errorf("failed to vacuum database: %v", err)
	}

	utils.Logger.Debug("Successfully copied data between stages",
		zap.Reflect("database", database),
		zap.Uint("sourceStage", sourceStage),
		zap.Uint("targetStage", targetStage),
	)

	return nil
}

func GetConnectionStringForStage(database Database, stage uint) (string, error) {
	name := database.GetName()

	switch stage {
	case utils.Config.Storage.Memory.StageNumber:
		return fmt.Sprintf("file:/%s?vfs=memory", name), nil
	case utils.Config.Storage.Local.StageNumber:
		path := getLocalPath(name)
		return fmt.Sprintf("file:%s?vfs=disk", path), nil
	case utils.Config.Storage.Remote.StageNumber:
		dbName := name
		if !strings.HasSuffix(dbName, ".db") {
			dbName += ".db"
		}
		return fmt.Sprintf("file:%s?vfs=r2", dbName), nil
	default:
		return "", fmt.Errorf("invalid stage: %d", stage)
	}
}

func copyFromMemoryStage(sourceDB *sql.DB, targetConn string) error {
	// NOTE: for memory databases, we need to use a temporary file onto which we can vacuum the data, and then copy that file to the target VFS.
	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("temp_copy_%d.db", os.Getpid()))
	defer os.Remove(tempFile)

	_, err := sourceDB.Exec("VACUUM INTO ?", tempFile)
	if err != nil {
		return fmt.Errorf("failed to vacuum memory database to temp file: %v", err)
	}

	tempConn := fmt.Sprintf("file:%s?vfs=disk", tempFile)
	tempDB, err := sql.Open("sqlite3", tempConn)
	if err != nil {
		return fmt.Errorf("failed to open temp database: %v", err)
	}
	defer tempDB.Close()

	targetPath := extractPathFromConnectionString(targetConn)

	// Remove target file if it exists to avoid "output file already exists" error
	if _, err := os.Stat(targetPath); err == nil {
		if err := os.Remove(targetPath); err != nil {
			return fmt.Errorf("failed to remove existing target file %s: %v", targetPath, err)
		}
	}

	_, err = tempDB.Exec("VACUUM INTO ?", targetPath)
	if err != nil {
		return fmt.Errorf("failed to vacuum temp database to target: %v", err)
	}

	return nil
}

func getLocalPath(name string) string {
	return fmt.Sprintf("./storage/%s.db", name)
}

func getTargetPath(name string, targetStage uint) string {
	switch targetStage {
	case utils.Config.Storage.Memory.StageNumber:
		return fmt.Sprintf("/%s", name)
	case utils.Config.Storage.Local.StageNumber:
		return getLocalPath(name)
	case utils.Config.Storage.Remote.StageNumber:
		if strings.HasSuffix(name, ".db") {
			return name
		}
		return fmt.Sprintf("%s.db", name)
	default:
		return name
	}
}

func extractPathFromConnectionString(connStr string) string {
	if strings.HasPrefix(connStr, "file:") {
		parts := strings.Split(connStr, "?")
		if len(parts) > 0 {
			return strings.TrimPrefix(parts[0], "file:")
		}
	}
	return connStr
}

func copyAcrossVFS(sourceDB *sql.DB, targetConn string) error {
	// Open the target database connection
	targetDB, err := sql.Open("sqlite3", targetConn)
	if err != nil {
		return fmt.Errorf("failed to open target database: %v", err)
	}
	defer targetDB.Close()

	// Test target connection
	err = targetDB.Ping()
	if err != nil {
		return fmt.Errorf("failed to ping target database: %v", err)
	}

	// Get all table names from source
	rows, err := sourceDB.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return fmt.Errorf("failed to get table list: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return fmt.Errorf("failed to scan table name: %v", err)
		}
		tables = append(tables, tableName)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating table names: %v", err)
	}

	// Begin transaction on target
	tx, err := targetDB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			utils.Logger.Warn("failed to rollback transaction", zap.Error(err))
		}
	}()

	// Copy schema first - get CREATE statements
	for _, table := range tables {
		var createSQL string
		err := sourceDB.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&createSQL)
		if err != nil {
			return fmt.Errorf("failed to get create statement for table %s: %v", table, err)
		}

		// Drop the table if it exists to avoid conflicts
		_, err = tx.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
		if err != nil {
			return fmt.Errorf("failed to drop existing table %s: %v", table, err)
		}

		_, err = tx.Exec(createSQL)
		if err != nil {
			return fmt.Errorf("failed to create table %s: %v", table, err)
		}
	}

	// Copy data for each table
	for _, table := range tables {
		// Get column names
		columnRows, err := sourceDB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			return fmt.Errorf("failed to get table info for %s: %v", table, err)
		}

		var columns []string
		for columnRows.Next() {
			var cid int
			var name, dataType string
			var notNull, pk int
			var defaultValue interface{}

			if err := columnRows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
				columnRows.Close()
				return fmt.Errorf("failed to scan column info: %v", err)
			}
			columns = append(columns, name)
		}
		columnRows.Close()

		if len(columns) == 0 {
			continue
		}

		// Build INSERT statement with quoted identifiers for safety
		quotedColumns := make([]string, len(columns))
		for i, col := range columns {
			quotedColumns[i] = `"` + strings.ReplaceAll(col, `"`, `""`) + `"`
		}
		columnList := strings.Join(quotedColumns, ", ")
		placeholders := strings.Repeat("?, ", len(columns)-1) + "?"
		quotedTable := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quotedTable, columnList, placeholders) // #nosec G201 - table/column names from schema, using placeholders for values

		// Prepare insert statement
		stmt, err := tx.Prepare(insertSQL)
		if err != nil {
			return fmt.Errorf("failed to prepare insert statement for table %s: %v", table, err)
		}

		// Copy all rows
		dataRows, err := sourceDB.Query(fmt.Sprintf("SELECT %s FROM %s", columnList, table))
		if err != nil {
			stmt.Close()
			return fmt.Errorf("failed to select data from table %s: %v", table, err)
		}

		for dataRows.Next() {
			values := make([]interface{}, len(columns))
			valuePtrs := make([]interface{}, len(columns))
			for i := range values {
				valuePtrs[i] = &values[i]
			}

			if err := dataRows.Scan(valuePtrs...); err != nil {
				dataRows.Close()
				stmt.Close()
				return fmt.Errorf("failed to scan row from table %s: %v", table, err)
			}

			if _, err := stmt.Exec(values...); err != nil {
				dataRows.Close()
				stmt.Close()
				return fmt.Errorf("failed to insert row into table %s: %v", table, err)
			}
		}

		dataRows.Close()
		stmt.Close()

		if err := dataRows.Err(); err != nil {
			return fmt.Errorf("error iterating data rows for table %s: %v", table, err)
		}
	}

	// Copy indexes and other schema objects
	indexRows, err := sourceDB.Query("SELECT sql FROM sqlite_master WHERE type='index' AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return fmt.Errorf("failed to get index list: %v", err)
	}
	defer indexRows.Close()

	for indexRows.Next() {
		var indexSQL string
		if err := indexRows.Scan(&indexSQL); err != nil {
			return fmt.Errorf("failed to scan index SQL: %v", err)
		}

		if _, err := tx.Exec(indexSQL); err != nil {
			utils.Logger.Warn("Failed to create index, continuing", zap.String("sql", indexSQL), zap.Error(err))
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %v", err)
	}

	utils.Logger.Debug("Successfully copied database across VFS systems")
	return nil
}

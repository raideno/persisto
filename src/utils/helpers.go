package utils

import (
	"database/sql"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

type QueryResultType []map[string]interface{}
type ExecResultType map[string]interface{}

func QueryResultToMaps(rows *sql.Rows) (QueryResultType, error) {
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results QueryResultType

	for rows.Next() {
		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))

		for i := range values {
			valuePtrs[i] = &values[i]
		}

		err := rows.Scan(valuePtrs...)
		if err != nil {
			return nil, err
		}

		rowMap := make(map[string]interface{})
		for i, col := range cols {
			val := values[i]

			if b, ok := val.([]byte); ok {
				rowMap[col] = string(b)
			} else if intVal, ok := val.(int64); ok {
				rowMap[col] = float64(intVal)
			} else {
				rowMap[col] = val
			}
		}

		results = append(results, rowMap)
	}

	return results, nil
}

func ExecResultToMap(result sql.Result) (ExecResultType, error) {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}

	lastInsertID, err := result.LastInsertId()
	if err != nil {
		lastInsertID = 0
	}

	return ExecResultType{
		"RowsAffected":  rowsAffected,
		"LastInsertID": lastInsertID,
	}, nil
}

func IsWriteOperation(query string) bool {
	query = strings.ToUpper(strings.TrimSpace(query))
	writeOperations := []string{"INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER"}

	for _, op := range writeOperations {
		if strings.HasPrefix(query, op) {
			return true
		}
	}
	return false
}

func VerifyDatabaseIntegrity(connectionString string) error {
	db, err := sql.Open("sqlite3", connectionString)
	if err != nil {
		return fmt.Errorf("failed to open database for integrity check: %v", err)
	}
	defer db.Close()

	err = db.Ping()
	if err != nil {
		return fmt.Errorf("database ping failed during integrity check: %v", err)
	}

	_, err = db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return fmt.Errorf("failed to query sqlite_master: %v", err)
	}

	var result string
	err = db.QueryRow("PRAGMA integrity_check").Scan(&result)
	if err != nil {
		return fmt.Errorf("failed to run integrity check: %v", err)
	}

	if result != "ok" {
		return fmt.Errorf("database integrity check failed: %s", result)
	}

	Logger.Info(
		"Database integrity check passed",
		zap.String("connectionString", connectionString),
	)

	return nil
}

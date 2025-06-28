package routes

import (
	"context"
	"net/http"

	"persisto/src/internal/databases"
	"persisto/src/internal/stages"
	"persisto/src/utils"

	huma "github.com/danielgtaylor/huma/v2"
)

func RegisterHealthRoutes(api huma.API) {
	type HealthOutput struct {
		Body struct {
			Status  string `json:"status" example:"ok"`
			Version string `json:"version,omitempty" example:"1.0.0"`
		}
	}
	huma.Register(
		api,
		huma.Operation{
			OperationID: "health-check",
			Method:      http.MethodGet,
			Path:        "/health",
			Summary:     "Health check endpoint",
			Description: "Returns the health status of the application",
			Tags:        []string{"health"},
		},
		func(ctx context.Context, input *struct{}) (*HealthOutput, error) {
			resp := &HealthOutput{}
			resp.Body.Status = "ok"
			if utils.Config != nil && utils.Config.Server.Version != "" {
				resp.Body.Version = utils.Config.Server.Version
			}
			return resp, nil
		},
	)
}

func RegisterDatabasesRoutes(api huma.API) {
	type ListDatabasesInput struct{}
	type DatabaseInfo struct {
		Name           string `json:"name"`
		Stage          uint   `json:"stage"`
		LastAccessedAt string `json:"last_accessed_at"`
		RequestCount   uint   `json:"request_count"`
	}
	type ListDatabasesOutput struct {
		Body struct {
			Databases []DatabaseInfo `json:"databases"`
		}
	}
	huma.Register(
		api,
		huma.Operation{
			OperationID: "list-databases",
			Method:      http.MethodGet,
			Path:        "/databases",
			Summary:     "List databases.",
			Description: "List all the available databases.",
			Tags:        []string{"databases"},
		},
		func(ctx context.Context, input *ListDatabasesInput) (*ListDatabasesOutput, error) {
			databases := databases.Dbs

			if databases == nil {
				return nil, &huma.ErrorModel{
					Status: http.StatusInternalServerError,
					Title:  "Initialization Error",
					Detail: "Databases weren't initialized.",
				}
			}

			response := &ListDatabasesOutput{}

			for _, db := range databases.Items {
				dbInfo := DatabaseInfo{
					Name:           db.GetName(),
					Stage:          db.GetStage(),
					LastAccessedAt: db.GetLastAccessed().Format("2006-01-02T15:04:05Z07:00"),
					RequestCount:   db.GetRequestCount(),
				}
				response.Body.Databases = append(response.Body.Databases, dbInfo)
			}

			return response, nil
		},
	)

	type CreateDatabaseInput struct {
		Body struct {
			Name string `json:"name" minLength:"1"  maxLength:"128" example:"production-db" doc:"Database name"`
		}
	}
	type CreateDatabaseOutput struct {
		Body struct {
			Database *databases.Database
		}
	}
	huma.Register(
		api,
		huma.Operation{
			OperationID: "create-database",
			Method:      http.MethodPost,
			Path:        "/databases",
			Summary:     "Create a database.",
			Description: "Create a database.",
			Tags:        []string{"databases"},
		},
		func(ctx context.Context, input *CreateDatabaseInput) (*CreateDatabaseOutput, error) {
			name := input.Body.Name

			_, err := databases.Dbs.FindByName(name)

			if err == nil {
				return nil, &huma.ErrorModel{
					Status: http.StatusConflict,
					Title:  "Database already exists.",
					Detail: "A database with this name already exists.",
				}
			}

			database, err := databases.Dbs.CreateDatabaseAndInitialize(name, stages.GetConfigDefaultStage())

			if err != nil {
				return nil, &huma.ErrorModel{
					Status: http.StatusInternalServerError,
					Title:  "Failed to create the Database.",
					Detail: "Failed to create the database.",
				}
			}

			response := &CreateDatabaseOutput{}

			response.Body.Database = database

			return response, nil
		},
	)

	type QueryDatabaseInput struct {
		Name string `path:"name"`
		Body struct {
			Queries []string `json:"queries" minItems:"1" maxItems:"16" example:"INSERT INTO users (name) VALUES ('Alice');"`
		}
	}
	type QueryResult struct {
		Success bool                  `json:"success"`
		Data    utils.QueryResultType `json:"data,omitempty"`
		Error   string                `json:"error,omitempty"`
	}
	type QueryDatabaseOutput struct {
		Body struct {
			Results []QueryResult `json:"results"`
		}
	}
	huma.Register(
		api,
		huma.Operation{
			OperationID: "database-query",
			Method:      http.MethodPost,
			Path:        "/databases/{name}/query",
			Summary:     "Execute a read query on a database.",
			Description: "Execute a read query on a database.",
			Tags:        []string{"databases"},
		},
		func(ctx context.Context, input *QueryDatabaseInput) (*QueryDatabaseOutput, error) {
			name := input.Name

			database, err := databases.Dbs.FindByName(name)
			if err != nil {
				return nil, &huma.ErrorModel{
					Status: http.StatusInternalServerError,
					Title:  "Database not found.",
					Detail: "Invalid database name provided.",
				}
			}

			response := &QueryDatabaseOutput{}
			results := make([]QueryResult, len(input.Body.Queries))

			type queryJob struct {
				index int
				query string
			}

			type queryResponse struct {
				index  int
				result utils.QueryResultType
				err    error
			}

			jobs := make(chan queryJob, len(input.Body.Queries))
			responses := make(chan queryResponse, len(input.Body.Queries))

			// TODO: make number of workers configurable
			numWorkers := 10
			if len(input.Body.Queries) < numWorkers {
				numWorkers = len(input.Body.Queries)
			}

			for w := 0; w < numWorkers; w++ {
				go func() {
					for job := range jobs {
						result, err := database.Query(job.query)
						responses <- queryResponse{
							index:  job.index,
							result: result,
							err:    err,
						}
					}
				}()
			}

			for i, query := range input.Body.Queries {
				jobs <- queryJob{index: i, query: query}
			}
			close(jobs)

			for i := 0; i < len(input.Body.Queries); i++ {
				resp := <-responses
				if resp.err != nil {
					results[resp.index] = QueryResult{
						Success: false,
						Error:   resp.err.Error(),
					}
				} else {
					results[resp.index] = QueryResult{
						Success: true,
						Data:    resp.result,
					}
				}
			}

			response.Body.Results = results
			return response, nil
		},
	)

	type ExecuteDatabaseInput struct {
		Name string `path:"name"`
		Body struct {
			// TODO: make minItems and maxItems configurable
			Queries []string `json:"queries" minItems:"1" maxItems:"16" example:"INSERT INTO users (name) VALUES ('Alice');"`
		}
	}
	type ExecuteResult struct {
		Success bool                 `json:"success"`
		Data    utils.ExecResultType `json:"data,omitempty"`
		Error   string               `json:"error,omitempty"`
	}
	type ExecuteDatabaseOutput struct {
		Body struct {
			Results []ExecuteResult `json:"results"`
		}
	}
	huma.Register(
		api,
		huma.Operation{
			OperationID: "database-execute",
			Method:      http.MethodPost,
			Path:        "/databases/{name}/execute",
			Summary:     "Execute a write query on a database.",
			Description: "Execute a write query on a database.",
			Tags:        []string{"databases"},
		},
		func(ctx context.Context, input *ExecuteDatabaseInput) (*ExecuteDatabaseOutput, error) {
			name := input.Name

			database, err := databases.Dbs.FindByName(name)
			if err != nil {
				return nil, &huma.ErrorModel{
					Status: http.StatusInternalServerError,
					Title:  "Database not found.",
					Detail: "Invalid database name provided.",
				}
			}

			response := &ExecuteDatabaseOutput{}

			for _, query := range input.Body.Queries {
				result, err := database.Execute(query)

				if err != nil {
					response.Body.Results = append(response.Body.Results, ExecuteResult{
						Success: false,
						Error:   err.Error(),
					})
				} else {
					response.Body.Results = append(response.Body.Results, ExecuteResult{
						Success: true,
						Data:    result,
					})
				}
			}

			return response, nil
		},
	)
}

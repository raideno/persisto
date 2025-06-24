package main

import (
	"fmt"
	"net/http"
	"time"

	"persisto/src/internal"
	"persisto/src/internal/databases"
	"persisto/src/internal/stages"
	"persisto/src/routes"
	"persisto/src/utils"
	"persisto/src/vfs"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func init() {
	_, err := utils.SetupConfiguration()
	if err != nil {
		fmt.Println("Failed to setup configuration.")
		panic(err)
	}

	_, err = utils.SetupLogger(zapcore.Level(utils.Config.Logging.Level))
	if err != nil {
		fmt.Println("Failed to setup logger.")
		panic("Failed to setup logger.")
	}

	err = vfs.RegisterVfs()
	if err != nil {
		fmt.Println("Failed to setup logger.")
		panic(err)
	}

	_, err = databases.SetupDatabases()
	if err != nil {
		fmt.Println("Failed to setup logger.")
		panic(err)
	}
}

func main() {
	utils.Logger.Debug("config.", zap.Reflect("config", utils.Config))
	
	stages.SetupStages()
	internal.SetupStagesMonitoring()

	router := chi.NewRouter()

	config := huma.DefaultConfig(
		utils.Config.Server.Information.Name,
		utils.Config.Server.Version,
	)
	config.Info.Description = utils.Config.Server.Information.Description
	config.Info.Contact = &huma.Contact{
		Name:  utils.Config.Server.Information.Contact.Name,
		Email: utils.Config.Server.Information.Contact.Email,
	}

	api := humachi.New(router, config)

	routes.RegisterHealthRoutes(api)
	routes.RegisterDatabasesRoutes(api)

	utils.Logger.Info("Server listening.", zap.Int("port", utils.Config.Server.Port))

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", utils.Config.Server.Port),
		Handler:      router,
		ReadTimeout:  time.Duration(utils.Config.Server.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(utils.Config.Server.WriteTimeout) * time.Second,
		IdleTimeout:  time.Duration(utils.Config.Server.IdleTimeout) * time.Second,
	}

	err := server.ListenAndServe()

	if err != nil {
		utils.Logger.Fatal("Failed to start server.", zap.Error(err))
		panic(err)
	}
}

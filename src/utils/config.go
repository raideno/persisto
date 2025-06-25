package utils

import (
	"fmt"
	"os"
	"sync"

	env "github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
	"go.uber.org/zap/zapcore"
)

type LogLevel zapcore.Level

func (logLevel *LogLevel) Set(text []byte) error {
	level := new(zapcore.Level)
	if err := level.UnmarshalText(text); err != nil {
		return err
	}
	*logLevel = LogLevel(*level)
	return nil
}

func (logLevel *LogLevel) UnmarshalText(text []byte) error {
	return logLevel.Set(text)
}

type Configuration struct {
	Server struct {
		Port        int    `env:"PORT" envDefault:"8080" validate:"gt=0"`
		Version     string `env:"VERSION" envDefault:"1.0.0"`
		Information struct {
			Name        string `env:"NAME" envDefault:"SQLite Backend API"`
			Description string `env:"DESCRIPTION" envDefault:"API for managing SQLite databases and monitoring stages."`
			Contact     struct {
				Name  string `env:"CONTACT_NAME" envDefault:"Unknown"`
				Email string `env:"CONTACT_EMAIL" envDefault:"unspecified"`
			}
		}

		ReadTimeout  int `env:"READ_TIMEOUT_SECONDS" envDefault:"10" validate:"gt=0"`
		WriteTimeout int `env:"WRITE_TIMEOUT_SECONDS" envDefault:"10" validate:"gt=0"`
		IdleTimeout  int `env:"IDLE_TIMEOUT_SECONDS" envDefault:"15" validate:"gt=0"`
	} `envPrefix:"SERVER_"`

	Logging struct {
		Level          LogLevel `env:"LEVEL" envDefault:"info"`
		OutputFilePath string   `env:"OUTPUT_FILE_PATH" envDefault:"logs.log"`
	} `envPrefix:"LOGGING_"`

	Settings struct {
		AutoStageMovement            bool `env:"AUTO_STAGE_MOVEMENT" envDefault:"true"`
		DefaultDatabaseCreationStage uint `env:"DEFAULT_DATABASE_CREATION_STAGE" envDefault:"3" validate:"gt=0"`
		PersistenceStage             uint `env:"PERSISTENCE_STAGE" envDefault:"3" validate:"gt=0"`
		StageTimeoutSeconds          int  `env:"STAGE_TIMEOUT_SECONDS" envDefault:"300" validate:"gt=0"`
		RequestCountThreshold        uint `env:"REQUEST_COUNT_THRESHOLD" envDefault:"2" validate:"gt=0"`
		AutoSyncEnabled              bool `env:"AUTO_SYNC_ENABLED" envDefault:"true"`
	} `envPrefix:"SETTINGS_"`

	Storage struct {
		Memory struct {
			Name        string `env:"NAME" envDefault:"Memory Storage"`
			StageNumber uint   `envDefault:"1" validate:"gt=0"`
		} `envPrefix:"STORAGE_MEMORY_"`

		Local struct {
			Name          string `env:"NAME" envDefault:"Local Storage"`
			StageNumber   uint   `envDefault:"2" validate:"gt=0"`
			DirectoryPath string `env:"DIRECTORY_PATH" envDefault:"./storage"`
		} `envPrefix:"STORAGE_LOCAL_"`

		Remote struct {
			Name        string `env:"NAME" envDefault:"Remote Storage"`
			StageNumber uint   `envDefault:"3" validate:"gt=0"`

			Enabled string

			AccessKeyID string `env:"ACCESS_KEY_ID"`
			SecretKey   string `env:"SECRET_KEY"`
			BucketName  string `env:"BUCKET_NAME"`
			Endpoint    string `env:"ENDPOINT"`
			Region      string `env:"REGION" envDefault:"auto"`
		} `envPrefix:"STORAGE_REMOTE_"`
	}
}

var (
	Config                  *Configuration
	ConfigurationSetupError error

	configurationSetupOnce sync.Once
)

func SetupConfiguration() (*Configuration, error) {
	configurationSetupOnce.Do(func() {
		if err := godotenv.Load(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: .env file not found or failed to load: %v\n", err)
		}

		cfg := &Configuration{}
		if err := env.Parse(cfg); err != nil {
			ConfigurationSetupError = err
			return
		}
		Config = cfg
	})
	return Config, ConfigurationSetupError
}

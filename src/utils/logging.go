package utils

import (
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	Logger           *zap.Logger
	LoggerSetupError error

	loggerSetupOnce sync.Once
)

var LOG_FILE_PATH string = "logs.log"

func SetupLogger(level zapcore.Level) (*zap.Logger, error) {
	loggerSetupOnce.Do(func() {
		consoleEncodingConfig := zap.NewDevelopmentEncoderConfig()
		consoleEncodingConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		consoleEncodingConfig.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05")
		consoleEncoder := zapcore.NewConsoleEncoder(consoleEncodingConfig)

		fileEncoderConfig := zap.NewProductionEncoderConfig()
		fileEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		fileEncoder := zapcore.NewJSONEncoder(fileEncoderConfig)

		logFile, e := os.OpenFile(LOG_FILE_PATH, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if e != nil {
			LoggerSetupError = e
			return
		}

		consoleOutput := zapcore.Lock(os.Stdout)
		fileOutput := zapcore.AddSync(logFile)

		core := zapcore.NewTee(
			zapcore.NewCore(consoleEncoder, consoleOutput, level),
			zapcore.NewCore(fileEncoder, fileOutput, level),
		)

		Logger = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	})

	return Logger, LoggerSetupError
}

package logger

import (
	"log"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	zapLogger *zap.Logger
	sugar     *zap.SugaredLogger
)

type Config struct {
	Dir        string
	MaxSizeMB  int
	MaxAgeDays int
	MaxBackups int
	Console    bool
	// Level is the minimum level written to app.log. The zero value is
	// InfoLevel, which keeps the codebase's many Debug diagnostics (terminal
	// io, git/file watchers, dispatchers) out of the production log; set
	// DebugLevel to turn them back on for troubleshooting.
	Level zapcore.Level
}

func Init(cfg Config) {
	if cfg.Dir == "" {
		cfg.Dir = "./logs"
	}
	if cfg.MaxSizeMB == 0 {
		cfg.MaxSizeMB = 100
	}
	if cfg.MaxAgeDays == 0 {
		cfg.MaxAgeDays = 7
	}
	if cfg.MaxBackups == 0 {
		cfg.MaxBackups = 10
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		log.Fatalf("logger: failed to create log dir %s: %v", cfg.Dir, err)
	}

	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	appRotator := &lumberjack.Logger{
		Filename:   filepath.Join(cfg.Dir, "app.log"),
		MaxSize:    cfg.MaxSizeMB,
		MaxAge:     cfg.MaxAgeDays,
		MaxBackups: cfg.MaxBackups,
		LocalTime:  true,
		Compress:   true,
	}

	errRotator := &lumberjack.Logger{
		Filename:   filepath.Join(cfg.Dir, "error.log"),
		MaxSize:    cfg.MaxSizeMB,
		MaxAge:     cfg.MaxAgeDays,
		MaxBackups: cfg.MaxBackups,
		LocalTime:  true,
		Compress:   true,
	}

	fileEncoder := zapcore.NewConsoleEncoder(encoderCfg)

	appCore := zapcore.NewCore(fileEncoder, zapcore.AddSync(appRotator), cfg.Level)
	errCore := zapcore.NewCore(fileEncoder, zapcore.AddSync(errRotator), zap.ErrorLevel)

	cores := []zapcore.Core{appCore, errCore}

	if cfg.Console {
		consoleCfg := encoderCfg
		consoleCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		consoleCore := zapcore.NewCore(
			zapcore.NewConsoleEncoder(consoleCfg),
			zapcore.AddSync(os.Stdout),
			zap.DebugLevel,
		)
		cores = append(cores, consoleCore)
	}

	core := zapcore.NewTee(cores...)
	zapLogger = zap.New(core,
		zap.AddCaller(),
		zap.AddCallerSkip(1),
		zap.AddStacktrace(zap.ErrorLevel),
	)
	sugar = zapLogger.Sugar()

	stdWriter, _ := zap.NewStdLogAt(zapLogger.WithOptions(zap.AddCallerSkip(1)), zap.InfoLevel)
	if stdWriter != nil {
		log.SetOutput(stdWriter.Writer())
		log.SetFlags(0)
	}
}

func Sync() {
	if zapLogger != nil {
		_ = zapLogger.Sync()
	}
}

func Close() {
	Sync()
	if zapLogger != nil {
		_ = zapLogger.Sync()
		zapLogger = nil
		sugar = nil
	}
}

func Debug(format string, args ...any) { getSugar().Debugf(format, args...) }
func Info(format string, args ...any)  { getSugar().Infof(format, args...) }
func Warn(format string, args ...any)  { getSugar().Warnf(format, args...) }
func Error(format string, args ...any) { getSugar().Errorf(format, args...) }
func Fatal(format string, args ...any) { getSugar().Fatalf(format, args...) }

func getSugar() *zap.SugaredLogger {
	if sugar != nil {
		return sugar
	}
	l, _ := zap.NewDevelopment()
	return l.Sugar().WithOptions(zap.AddCallerSkip(2))
}

func init() {
	l, _ := zap.NewDevelopment()
	zapLogger = l
	sugar = l.Sugar()
}

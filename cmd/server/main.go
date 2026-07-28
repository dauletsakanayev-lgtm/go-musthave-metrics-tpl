package main

import (
	"crypto/rsa"
	"database/sql"
	"net"
	"os"

	"github.com/bluegopher/go-musthave-metrics-tpl/internal/audit"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/buildinfo"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/crypto"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/logger"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/server"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/storage"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
)

// Сведения о сборке. Значения по умолчанию можно перезаписать при компиляции
// через -ldflags "-X main.buildVersion=... -X main.buildDate=... -X main.buildCommit=...".
// Подробнее — в README проекта.
var (
	buildVersion = "N/A"
	buildDate    = "N/A"
	buildCommit  = "N/A"
)

func main() {
	buildinfo.Print(os.Stdout, buildVersion, buildDate, buildCommit)

	cfg, err := parseConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("ошибка конфигурации")
	}

	if err := logger.Initialize(cfg.LogLevel); err != nil {
		log.Fatal().Err(err).Msg("ошибка инициализации логгера")
	}

	var repo storage.Repository
	var db *sql.DB
	// memRepo сохраняется отдельно, чтобы можно было выполнить финальный
	// сброс в файл после graceful shutdown HTTP-сервера.
	var memRepo *storage.MemoryStorage

	if cfg.DatabaseDSN != "" {
		var err error
		db, err = storage.NewPostgresDB(cfg.DatabaseDSN)
		if err != nil {
			log.Fatal().Err(err).Msg("ошибка при подключения к БД")
		}
		repo = storage.NewPostgresStorege(db)
	} else {
		memRepo = storage.NewMemoryStorage()

		// загрузка метрик из файла при старте
		if cfg.Restore && cfg.FilePath != "" {
			if err := storage.LoadFromFile(memRepo, cfg.FilePath); err != nil {
				log.Error().Err(err).Msg("Не удалось загрузить метрик")
			}
		}
		// периодическое сохранение на диск
		if cfg.FilePath != "" && cfg.StoreInterval > 0 {
			go storage.RunSaver(memRepo, cfg.FilePath, cfg.StoreInterval)
		}
		//синхронная запись — передаём filePath в сервер
		if cfg.FilePath != "" && cfg.StoreInterval == 0 {
			memRepo.SetSyncFile(cfg.FilePath)
		}
		repo = memRepo
	}

	// Аудит запросов (паттерн «Наблюдатель»): приёмники подключаются,
	// только если задан соответствующий параметр конфигурации.
	auditPub := audit.NewPublisher()
	if cfg.AuditFile != "" {
		fs, err := audit.NewFileSink(cfg.AuditFile)
		if err != nil {
			log.Fatal().Err(err).Msg("ошибка открытия файла аудита")
		}
		auditPub.Subscribe(fs)
		log.Info().Str("file", cfg.AuditFile).Msg("аудит в файл включён")
	}
	if cfg.AuditURL != "" {
		auditPub.Subscribe(audit.NewHTTPSink(cfg.AuditURL))
		log.Info().Str("url", cfg.AuditURL).Msg("аудит по сети включён")
	}
	defer func() {
		if err := auditPub.Close(); err != nil {
			log.Error().Err(err).Msg("ошибка закрытия приёмников аудита")
		}
	}()

	var privateKey *rsa.PrivateKey
	if cfg.CryptoKey != "" {
		privateKey, err = crypto.LoadPrivateKey(cfg.CryptoKey)
		if err != nil {
			log.Fatal().Err(err).Msg("ошибка загрузки приватного ключа")
		}
		log.Info().Str("path", cfg.CryptoKey).Msg("расшифровка трафика включена")
	}

	// gRPC-сервер поднимается параллельно с HTTP, если задан адрес.
	// Останавливается через GracefulStop после завершения HTTP-цикла (по SIGINT).
	var grpcSrv *grpc.Server
	if cfg.GRPCAddress != "" {
		lis, err := net.Listen("tcp", cfg.GRPCAddress)
		if err != nil {
			log.Fatal().Err(err).Str("addr", cfg.GRPCAddress).Msg("не удалось открыть gRPC-порт")
		}
		grpcSrv, err = server.NewGRPCServer(repo, cfg.TrustedSubnet)
		if err != nil {
			log.Fatal().Err(err).Msg("ошибка инициализации gRPC-сервера")
		}
		go func() {
			log.Info().Str("addr", cfg.GRPCAddress).Msg("gRPC-сервер запущен")
			if err := grpcSrv.Serve(lis); err != nil {
				log.Error().Err(err).Msg("gRPC-сервер завершился с ошибкой")
			}
		}()
	}

	srv := server.New(cfg.Addr, repo, db, cfg.HashKey, auditPub, cfg.EnablePprof, privateKey, cfg.TrustedSubnet)
	if err := srv.Run(); err != nil {
		log.Fatal().Err(err).Msg("ошибка запуска сервера")
	}

	if grpcSrv != nil {
		log.Info().Msg("останавливаем gRPC-сервер")
		grpcSrv.GracefulStop()
	}

	// Graceful shutdown: срv.Run уже дождался завершения in-flight
	// HTTP-запросов. Осталось сбросить накопленные метрики в файл
	// (если хранилище in-memory и путь задан) и закрыть соединение с БД.
	if memRepo != nil && cfg.FilePath != "" {
		if err := storage.SaveToFile(memRepo, cfg.FilePath); err != nil {
			log.Error().Err(err).Msg("финальное сохранение метрик в файл не удалось")
		} else {
			log.Info().Str("file", cfg.FilePath).Msg("финальное сохранение метрик выполнено")
		}
	}
	if db != nil {
		if err := db.Close(); err != nil {
			log.Error().Err(err).Msg("ошибка закрытия соединения с БД")
		}
	}
}

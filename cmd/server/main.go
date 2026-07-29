package main

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/bluegopher/go-musthave-metrics-tpl/internal/audit"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/buildinfo"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/crypto"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/logger"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/server"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/storage"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
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

	// Единый контекст завершения: сигнал OS отменяет ctx, любая ошибка
	// внутри errgroup — тоже. Оба транспорта (HTTP и gRPC) получают
	// одно и то же событие остановки: падение одного не оставит второй
	// в подвешенном состоянии.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	g, gctx := errgroup.WithContext(ctx)

	// HTTP-сервер.
	srv := server.New(cfg.Addr, repo, db, cfg.HashKey, auditPub, cfg.EnablePprof, privateKey, cfg.TrustedSubnet)
	g.Go(func() error {
		return srv.Run(gctx)
	})

	// gRPC-сервер поднимается параллельно, если задан адрес.
	// Отдельная горутина-watcher дёргает GracefulStop при отмене gctx —
	// это позволяет errgroup узнать о сбое gRPC-транспорта (ошибка Serve)
	// и симметрично уронить HTTP.
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
		g.Go(func() error {
			log.Info().Str("addr", cfg.GRPCAddress).Msg("gRPC-сервер запущен")
			return grpcSrv.Serve(lis)
		})
		g.Go(func() error {
			<-gctx.Done()
			log.Info().Msg("останавливаем gRPC-сервер")
			grpcSrv.GracefulStop()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		log.Error().Err(err).Msg("завершение с ошибкой одного из транспортов")
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

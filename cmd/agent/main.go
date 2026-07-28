package main

import (
	"context"
	"crypto/rsa"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bluegopher/go-musthave-metrics-tpl/internal/agent"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/buildinfo"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/crypto"
	"github.com/rs/zerolog/log"
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

	baseURL := "http://" + cfg.Addr

	var publicKey *rsa.PublicKey
	if cfg.CryptoKey != "" {
		publicKey, err = crypto.LoadPublicKey(cfg.CryptoKey)
		if err != nil {
			log.Fatal().Err(err).Msg("ошибка загрузки публичного ключа")
		}
		log.Info().Str("path", cfg.CryptoKey).Msg("шифрование трафика включено")
	}

	// BatchSender — общий интерфейс HTTP- и gRPC-реализаций отправки.
	type BatchSender interface {
		SendBatch(gauges []agent.GaugeMetric, pollCountDelta int64) error
	}

	var sender BatchSender
	if cfg.Transport == "grpc" {
		if cfg.GRPCAddress == "" {
			log.Fatal().Msg("для transport=grpc нужен -grpc-address (или GRPC_ADDRESS)")
		}
		gs, err := agent.NewGRPCSender(cfg.GRPCAddress)
		if err != nil {
			log.Fatal().Err(err).Msg("не удалось подключиться к gRPC-серверу")
		}
		defer gs.Close()
		log.Info().Str("addr", cfg.GRPCAddress).Msg("транспорт метрик: gRPC")
		sender = gs
	} else {
		sender = agent.NewSender(baseURL, cfg.HashKey, publicKey)
		log.Info().Str("url", baseURL).Msg("транспорт метрик: HTTP")
	}
	store := agent.NewMetricsStore()

	// ctx отменяется при получении сигнала завершения (SIGINT/SIGTERM/SIGQUIT).
	// Все дочерние горутины (сборщики, отправитель) слушают его и завершаются
	// штатно, чтобы накопленные метрики не потерялись.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	// wg отслеживает все фоновые горутины отправки (worker pool),
	// чтобы main дождался их завершения перед выходом.
	var wg sync.WaitGroup

	// Сборщик runtime-метрик.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				store.UpdateGauges(agent.CollectGauges())
			}
		}
	}()

	// Сборщик gopsutil-метрик.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				store.UpdatePSMetrics(agent.CollectPSUtilMetrics())
			}
		}
	}()

	// Отправитель. Ограничение параллельных запросов через семафор,
	// wg — чтобы дождаться завершения всех запущенных отправок.
	sem := make(chan struct{}, cfg.RateLimit)
	reportTicker := time.NewTicker(cfg.ReportInterval)
	defer reportTicker.Stop()

	sendBatch := func(metrics []agent.GaugeMetric, count int64) {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := sender.SendBatch(metrics, count); err != nil {
				log.Error().Err(err).Msg("отправка метрик")
			}
		}()
	}

reportLoop:
	for {
		select {
		case <-ctx.Done():
			break reportLoop
		case <-reportTicker.C:
			all, ps := store.GetAll()
			sendBatch(all, ps)
		}
	}

	// Финальная отправка накопленных к моменту получения сигнала метрик.
	log.Info().Msg("получен сигнал завершения, финальная отправка метрик")
	all, ps := store.GetAll()
	if len(all) > 0 {
		sendBatch(all, ps)
	}

	// Ждём завершения всех сборщиков и отправителей.
	wg.Wait()
	log.Info().Msg("агент завершил работу корректно")
}

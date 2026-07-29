// Package server собирает HTTP-сервер сбора метрик: настраивает роутер chi,
// подключает middleware (логирование, gzip, проверку подписи), регистрирует
// эндпоинты практического трека и обеспечивает graceful shutdown.
package server

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"net/http"
	"time"

	"github.com/bluegopher/go-musthave-metrics-tpl/internal/audit"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/handlers"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/logger"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/middleware"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/service"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/storage"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
)

// Server — HTTP-сервер сбора метрик с зависимостями: адрес прослушивания,
// хранилище метрик, соединение с БД, ключ подписи, издатель аудита и флаг
// включения эндпоинтов профилирования pprof.
type Server struct {
	addr          string
	repo          storage.Repository
	db            *sql.DB
	hashKey       string
	auditPub      *audit.Publisher
	enablePprof   bool
	privateKey    *rsa.PrivateKey
	trustedSubnet string
}

// New создаёт сервер с заданным адресом, хранилищем, соединением с БД,
// ключом HMAC-подписи (пустой — подпись отключена), издателем аудита,
// флагом enablePprof, приватным RSA-ключом для расшифровки трафика
// (nil — расшифровка отключена) и CIDR доверенной подсети агентов
// (пусто — проверка X-Real-IP отключена).
func New(addr string, repo storage.Repository, db *sql.DB, hashKey string, auditPub *audit.Publisher, enablePprof bool, privateKey *rsa.PrivateKey, trustedSubnet string) *Server {
	return &Server{
		addr:          addr,
		repo:          repo,
		db:            db,
		hashKey:       hashKey,
		auditPub:      auditPub,
		enablePprof:   enablePprof,
		privateKey:    privateKey,
		trustedSubnet: trustedSubnet,
	}
}

// buildRouter собирает chi-роутер со всеми middleware и эндпоинтами сервера.
// Вынесен из Run отдельно, чтобы маршрутизацию можно было покрыть тестами
// без запуска блокирующего ListenAndServe.
func (s *Server) buildRouter() http.Handler {
	svc := service.NewMetricsService(s.repo)

	r := chi.NewRouter()
	r.Use(logger.RequestLogger)
	// Проверка доверенной подсети — до всех остальных обработок.
	// Отказ по IP не должен приводить к расшифровке или парсингу тела.
	if s.trustedSubnet != "" {
		trustedMW, err := middleware.TrustedSubnetMiddleware(s.trustedSubnet)
		if err != nil {
			log.Fatal().Err(err).Msg("некорректный CIDR trusted_subnet")
		}
		r.Use(trustedMW)
	}
	r.Use(middleware.GzipMiddleware)
	// Расшифровка выполняется сразу после распаковки gzip: агент шифрует
	// исходные данные, затем сжимает их; сервер идёт в обратном порядке.
	// Middleware ничего не делает, если privateKey == nil.
	if s.privateKey != nil {
		r.Use(middleware.DecryptMiddleware(s.privateKey))
	}
	// Эндпоинты pprof регистрируются только при явном включении:
	// они раскрывают внутренности процесса (стеки, heap) и не должны быть
	// доступны в production без ограничений. Включай в dev/staging при
	// профилировании (флаг --pprof / env PPROF).
	// go tool pprof http://<addr>/debug/pprof/heap
	if s.enablePprof {
		r.Mount("/debug", chimw.Profiler())
	}
	r.Post("/update/{type}/{name}/{value}", handlers.MetricsHandler(svc, s.auditPub))
	r.Post("/update/", handlers.UpdateJSONHandler(svc, s.auditPub))
	r.Get("/value/{type}/{name}", handlers.ValueHandler(svc))
	r.Post("/value/", handlers.ValueJSONHandler(svc))
	r.Get("/", handlers.ListHandler(svc))
	r.Get("/ping", handlers.PingHandler(s.db))
	r.Post("/updates/", handlers.UpdatesJSONHandler(svc, s.auditPub))
	if s.hashKey != "" {
		r.Use(middleware.HashCheckMiddleware(s.hashKey))
	}
	return r
}

// Run настраивает роутер и запускает HTTP-сервер, блокируясь до отмены
// переданного контекста, после чего выполняет graceful shutdown: даёт срок
// in-flight запросам завершиться, затем возвращает управление, чтобы main
// мог сохранить состояние и закрыть внешние ресурсы.
//
// Управление сигналами (SIGINT/SIGTERM/SIGQUIT) вынесено в main.go, где
// один signal.NotifyContext координирует HTTP и gRPC через errgroup —
// падение или сигнал одного транспорта отменяет ctx и останавливает второй.
//
// Возвращает ошибку от ListenAndServe (кроме штатного ErrServerClosed) или
// ошибку Shutdown; если сервер отработал корректно — nil.
func (s *Server) Run(ctx context.Context) error {
	r := s.buildRouter()

	srv := &http.Server{
		Addr:         s.addr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info().Str("addr", s.addr).Msg("HTTP-сервер запущен")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		// Слушатель упал (порт занят и т.п.) до сигнала отмены.
		return err
	case <-ctx.Done():
	}

	log.Info().Msg("останавливаем HTTP-сервер")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	log.Info().Msg("HTTP-сервер завершил работу корректно")
	return nil
}

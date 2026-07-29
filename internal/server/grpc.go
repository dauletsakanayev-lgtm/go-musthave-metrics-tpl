package server

import (
	"context"
	"fmt"
	"net"
	"strings"

	models "github.com/bluegopher/go-musthave-metrics-tpl/internal/model"
	pb "github.com/bluegopher/go-musthave-metrics-tpl/internal/proto"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/storage"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MetricsGRPCServer реализует сервис pb.MetricsServer поверх Repository.
type MetricsGRPCServer struct {
	pb.UnimplementedMetricsServer
	repo storage.Repository
}

// NewMetricsGRPCServer конструирует реализацию сервиса.
func NewMetricsGRPCServer(repo storage.Repository) *MetricsGRPCServer {
	return &MetricsGRPCServer{repo: repo}
}

// UpdateMetrics принимает батч метрик и сохраняет его через Repository.
func (s *MetricsGRPCServer) UpdateMetrics(ctx context.Context, req *pb.UpdateMetricsRequest) (*pb.UpdateMetricsResponse, error) {
	in := req.GetMetrics()
	batch := make([]models.Metrics, 0, len(in))
	for _, m := range in {
		metric := models.Metrics{ID: m.GetId()}
		switch m.GetType() {
		case pb.Metric_GAUGE:
			metric.MType = models.Gauge
			v := m.GetValue()
			metric.Value = &v
		case pb.Metric_COUNTER:
			metric.MType = models.Counter
			d := m.GetDelta()
			metric.Delta = &d
		default:
			// Тип из внешнего запроса — сообщать безопасно, помогает клиенту.
			return nil, status.Errorf(codes.InvalidArgument, "unknown metric type: %v", m.GetType())
		}
		batch = append(batch, metric)
	}
	if err := s.repo.UpdateBatch(ctx, batch); err != nil {
		// Внутренние ошибки хранилища могут содержать SQL / имена таблиц /
		// фрагменты DSN — наружу отдаём общий текст, детали остаются в логах.
		log.Error().Err(err).Msg("gRPC UpdateMetrics: repo.UpdateBatch failed")
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &pb.UpdateMetricsResponse{}, nil
}

// trustedSubnetInterceptor проверяет метаданные x-real-ip запроса на принадлежность
// доверенной подсети subnet. Пустая subnet отключает проверку.
func trustedSubnetInterceptor(subnet string) (grpc.UnaryServerInterceptor, error) {
	if subnet == "" {
		return nil, nil
	}
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("некорректный CIDR trusted_subnet: %w", err)
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.PermissionDenied, "нет метаданных")
		}
		vals := md.Get("x-real-ip")
		if len(vals) == 0 {
			return nil, status.Error(codes.PermissionDenied, "нет x-real-ip")
		}
		ip := net.ParseIP(strings.TrimSpace(vals[0]))
		if ip == nil || !ipNet.Contains(ip) {
			return nil, status.Error(codes.PermissionDenied, "ip не входит в доверенную подсеть")
		}
		return handler(ctx, req)
	}, nil
}

// NewGRPCServer создаёт grpc.Server с зарегистрированным сервисом Metrics
// и, при непустом trustedSubnet, — с интерцептором проверки подсети.
func NewGRPCServer(repo storage.Repository, trustedSubnet string) (*grpc.Server, error) {
	interceptor, err := trustedSubnetInterceptor(trustedSubnet)
	if err != nil {
		return nil, err
	}
	var opts []grpc.ServerOption
	if interceptor != nil {
		opts = append(opts, grpc.UnaryInterceptor(interceptor))
	}
	srv := grpc.NewServer(opts...)
	pb.RegisterMetricsServer(srv, NewMetricsGRPCServer(repo))
	return srv, nil
}

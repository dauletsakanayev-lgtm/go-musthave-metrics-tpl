package agent

import (
	"context"
	"fmt"
	"time"

	pb "github.com/bluegopher/go-musthave-metrics-tpl/internal/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// GRPCSender отправляет метрики на сервер через gRPC.
// Реализует ту же сигнатуру SendBatch, что и HTTP Sender.
type GRPCSender struct {
	conn    *grpc.ClientConn
	client  pb.MetricsClient
	localIP string
}

// NewGRPCSender подключается к gRPC-серверу по адресу addr
// (в формате "host:port", без схемы) и возвращает готового отправителя.
func NewGRPCSender(addr string) (*GRPCSender, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("подключение к gRPC-серверу: %w", err)
	}
	return &GRPCSender{
		conn:    conn,
		client:  pb.NewMetricsClient(conn),
		localIP: detectLocalIP(),
	}, nil
}

// Close закрывает соединение. Вызывается один раз при завершении агента.
func (s *GRPCSender) Close() error {
	return s.conn.Close()
}

// SendBatch собирает батч в pb.UpdateMetricsRequest и отправляет одним
// unary-вызовом. IP агента передаётся через metadata (x-real-ip),
// что позволяет серверу выполнить проверку доверенной подсети
// в UnaryInterceptor.
func (s *GRPCSender) SendBatch(gauges []GaugeMetric, pollCountDelta int64) error {
	if len(gauges) == 0 && pollCountDelta == 0 {
		return nil
	}

	metrics := make([]*pb.Metric, 0, len(gauges)+1)
	for _, g := range gauges {
		metrics = append(metrics, &pb.Metric{
			Id:    g.Name,
			Type:  pb.Metric_GAUGE,
			Value: g.Value,
		})
	}
	metrics = append(metrics, &pb.Metric{
		Id:    pollCountName,
		Type:  pb.Metric_COUNTER,
		Delta: pollCountDelta,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if s.localIP != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-real-ip", s.localIP)
	}

	_, err := s.client.UpdateMetrics(ctx, &pb.UpdateMetricsRequest{Metrics: metrics})
	if err != nil {
		return fmt.Errorf("gRPC UpdateMetrics: %w", err)
	}
	return nil
}

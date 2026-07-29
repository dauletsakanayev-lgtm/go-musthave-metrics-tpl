package server

import (
	"context"
	"net"
	"testing"

	pb "github.com/bluegopher/go-musthave-metrics-tpl/internal/proto"
	"github.com/bluegopher/go-musthave-metrics-tpl/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// dialBufconn поднимает *grpc.Server (готовый через NewGRPCServer) на bufconn
// и возвращает подключённый клиент. Всё завершается через t.Cleanup.
func dialBufconn(t *testing.T, srv *grpc.Server) pb.MetricsClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn server exited: %v", err)
		}
	}()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close()
		srv.GracefulStop()
	})
	return pb.NewMetricsClient(conn)
}

func TestMetricsGRPCServer_UpdateMetrics_MixedBatch(t *testing.T) {
	repo := storage.NewMemoryStorage()
	srv, err := NewGRPCServer(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	client := dialBufconn(t, srv)

	_, err = client.UpdateMetrics(context.Background(), &pb.UpdateMetricsRequest{
		Metrics: []*pb.Metric{
			{Id: "Alloc", Type: pb.Metric_GAUGE, Value: 123.5},
			{Id: "PollCount", Type: pb.Metric_COUNTER, Delta: 7},
		},
	})
	if err != nil {
		t.Fatalf("UpdateMetrics: %v", err)
	}

	if v, ok := repo.GetGauge(context.Background(), "Alloc"); !ok || v != 123.5 {
		t.Errorf("Alloc = %v, ok=%v; want 123.5, true", v, ok)
	}
	if v, ok := repo.GetCounter(context.Background(), "PollCount"); !ok || v != 7 {
		t.Errorf("PollCount = %v, ok=%v; want 7, true", v, ok)
	}
}

func TestMetricsGRPCServer_UpdateMetrics_InvalidType(t *testing.T) {
	repo := storage.NewMemoryStorage()
	srv, err := NewGRPCServer(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	client := dialBufconn(t, srv)

	_, err = client.UpdateMetrics(context.Background(), &pb.UpdateMetricsRequest{
		Metrics: []*pb.Metric{{Id: "x", Type: pb.Metric_MType(99)}},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка на неизвестный тип метрики")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("код ошибки: %v, ожидали InvalidArgument", status.Code(err))
	}
}

func TestMetricsGRPCServer_UpdateMetrics_EmptyBatch(t *testing.T) {
	repo := storage.NewMemoryStorage()
	srv, err := NewGRPCServer(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	client := dialBufconn(t, srv)

	_, err = client.UpdateMetrics(context.Background(), &pb.UpdateMetricsRequest{})
	if err != nil {
		t.Fatalf("пустой батч не должен приводить к ошибке: %v", err)
	}
}

func TestNewGRPCServer_InvalidCIDR(t *testing.T) {
	if _, err := NewGRPCServer(storage.NewMemoryStorage(), "not-a-cidr"); err == nil {
		t.Fatal("ожидалась ошибка на некорректный CIDR")
	}
}

func TestTrustedSubnetInterceptor(t *testing.T) {
	tests := []struct {
		name     string
		md       metadata.MD
		wantCode codes.Code
	}{
		{"in_subnet", metadata.Pairs("x-real-ip", "192.168.1.42"), codes.OK},
		{"out_of_subnet", metadata.Pairs("x-real-ip", "10.0.0.1"), codes.PermissionDenied},
		{"invalid_ip", metadata.Pairs("x-real-ip", "not-an-ip"), codes.PermissionDenied},
		{"no_header", metadata.Pairs("other", "x"), codes.PermissionDenied},
		{"no_metadata", nil, codes.PermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := storage.NewMemoryStorage()
			srv, err := NewGRPCServer(repo, "192.168.1.0/24")
			if err != nil {
				t.Fatal(err)
			}
			client := dialBufconn(t, srv)

			ctx := context.Background()
			if tt.md != nil {
				ctx = metadata.NewOutgoingContext(ctx, tt.md)
			}
			_, err = client.UpdateMetrics(ctx, &pb.UpdateMetricsRequest{})
			got := status.Code(err)
			if got != tt.wantCode {
				t.Errorf("код: %v, ожидали %v (err=%v)", got, tt.wantCode, err)
			}
		})
	}
}

// nil-интерцептор при пустом CIDR — путь без проверки должен работать.
func TestTrustedSubnetInterceptor_EmptyDisables(t *testing.T) {
	interceptor, err := trustedSubnetInterceptor("")
	if err != nil {
		t.Fatal(err)
	}
	if interceptor != nil {
		t.Fatal("пустой CIDR должен возвращать nil-интерцептор")
	}
}

package agent

import (
	"context"
	"net"
	"sync"
	"testing"

	pb "github.com/bluegopher/go-musthave-metrics-tpl/internal/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeMetricsServer сохраняет последний полученный запрос и его метаданные,
// чтобы тесты могли проверить, что именно отправил клиент.
type fakeMetricsServer struct {
	pb.UnimplementedMetricsServer
	mu      sync.Mutex
	lastReq *pb.UpdateMetricsRequest
	lastMD  metadata.MD
}

func (f *fakeMetricsServer) UpdateMetrics(ctx context.Context, req *pb.UpdateMetricsRequest) (*pb.UpdateMetricsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastReq = req
	md, _ := metadata.FromIncomingContext(ctx)
	f.lastMD = md
	return &pb.UpdateMetricsResponse{}, nil
}

// startFakeServer поднимает gRPC-сервер на localhost:0 и возвращает адрес.
func startFakeServer(t *testing.T) (addr string, fake *fakeMetricsServer) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	fake = &fakeMetricsServer{}
	pb.RegisterMetricsServer(srv, fake)
	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("fake server exited: %v", err)
		}
	}()
	t.Cleanup(func() { srv.GracefulStop() })
	return lis.Addr().String(), fake
}

func TestGRPCSender_SendBatch(t *testing.T) {
	addr, fake := startFakeServer(t)

	sender, err := NewGRPCSender(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	gauges := []GaugeMetric{
		{Name: "Alloc", Value: 123.5},
		{Name: "Sys", Value: 456.0},
	}
	if err := sender.SendBatch(gauges, 7); err != nil {
		t.Fatalf("SendBatch: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.lastReq == nil {
		t.Fatal("сервер не получил запрос")
	}
	got := fake.lastReq.GetMetrics()
	if len(got) != 3 { // 2 gauge + 1 counter
		t.Fatalf("получено %d метрик, ожидали 3", len(got))
	}
	// последней в батче идёт PollCount с типом COUNTER
	last := got[len(got)-1]
	if last.GetId() != pollCountName || last.GetType() != pb.Metric_COUNTER || last.GetDelta() != 7 {
		t.Errorf("PollCount = %+v", last)
	}
}

func TestGRPCSender_SendBatch_Empty(t *testing.T) {
	addr, fake := startFakeServer(t)
	sender, err := NewGRPCSender(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	if err := sender.SendBatch(nil, 0); err != nil {
		t.Fatalf("пустой батч не должен приводить к ошибке: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.lastReq != nil {
		t.Error("для пустого батча запрос не должен уходить на сервер")
	}
}

func TestGRPCSender_XRealIPHeader(t *testing.T) {
	addr, fake := startFakeServer(t)
	sender, err := NewGRPCSender(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	// Форсируем localIP, иначе detectLocalIP может вернуть "" в CI без сети.
	sender.localIP = "10.0.0.5"

	if err := sender.SendBatch([]GaugeMetric{{Name: "x", Value: 1}}, 0); err != nil {
		t.Fatalf("SendBatch: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	vals := fake.lastMD.Get("x-real-ip")
	if len(vals) != 1 || vals[0] != "10.0.0.5" {
		t.Errorf("x-real-ip: %v, ожидали [10.0.0.5]", vals)
	}
}

func TestGRPCSender_NoIPHeader(t *testing.T) {
	addr, fake := startFakeServer(t)
	sender, err := NewGRPCSender(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	sender.localIP = "" // явно, чтобы не зависеть от окружения

	if err := sender.SendBatch([]GaugeMetric{{Name: "x", Value: 1}}, 0); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if vals := fake.lastMD.Get("x-real-ip"); len(vals) != 0 {
		t.Errorf("не должно быть x-real-ip: %v", vals)
	}
}

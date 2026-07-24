package storage

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	models "github.com/bluegopher/go-musthave-metrics-tpl/internal/model"
)

// newMockRepo создаёт PostgresStorage с моком БД (не требует реальный PostgreSQL).
func newMockRepo(t *testing.T) (*PostgresStorage, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	return NewPostgresStorege(db), mock, func() { db.Close() }
}

func TestPostgresStorage_UpdateGauge(t *testing.T) {
	repo, mock, cleanup := newMockRepo(t)
	defer cleanup()

	mock.ExpectExec("INSERT INTO metrics").
		WithArgs("Alloc", 123.5).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := repo.UpdateGauge(context.Background(), "Alloc", 123.5); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPostgresStorage_UpdateCounter(t *testing.T) {
	repo, mock, cleanup := newMockRepo(t)
	defer cleanup()

	mock.ExpectExec("INSERT INTO metrics").
		WithArgs("PollCount", int64(10)).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := repo.UpdateCounter(context.Background(), "PollCount", 10); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestPostgresStorage_GetGauge(t *testing.T) {
	repo, mock, cleanup := newMockRepo(t)
	defer cleanup()

	mock.ExpectQuery("SELECT value FROM metrics").
		WithArgs("Alloc").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(3.14))

	v, ok := repo.GetGauge(context.Background(), "Alloc")
	if !ok || v != 3.14 {
		t.Fatalf("value=%v, ok=%v", v, ok)
	}
}

func TestPostgresStorage_GetGauge_NotFound(t *testing.T) {
	repo, mock, cleanup := newMockRepo(t)
	defer cleanup()

	mock.ExpectQuery("SELECT value FROM metrics").
		WithArgs("Missing").
		WillReturnRows(sqlmock.NewRows([]string{"value"}))

	if _, ok := repo.GetGauge(context.Background(), "Missing"); ok {
		t.Fatal("ожидали ok=false для отсутствующей метрики")
	}
}

func TestPostgresStorage_GetCounter(t *testing.T) {
	repo, mock, cleanup := newMockRepo(t)
	defer cleanup()

	mock.ExpectQuery("SELECT delta FROM metrics").
		WithArgs("PollCount").
		WillReturnRows(sqlmock.NewRows([]string{"delta"}).AddRow(int64(42)))

	v, ok := repo.GetCounter(context.Background(), "PollCount")
	if !ok || v != 42 {
		t.Fatalf("value=%v, ok=%v", v, ok)
	}
}

func TestPostgresStorage_GetAllGauges(t *testing.T) {
	repo, mock, cleanup := newMockRepo(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{"id", "value"}).
		AddRow("Alloc", 1.5).
		AddRow("Sys", 2.5)
	mock.ExpectQuery("SELECT id, value FROM metrics").WillReturnRows(rows)

	got := repo.GetAllGauges(context.Background())
	if len(got) != 2 || got["Alloc"] != 1.5 || got["Sys"] != 2.5 {
		t.Fatalf("получили %v", got)
	}
}

func TestPostgresStorage_GetAllCounters(t *testing.T) {
	repo, mock, cleanup := newMockRepo(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{"id", "delta"}).
		AddRow("PollCount", int64(5)).
		AddRow("Errors", int64(10))
	mock.ExpectQuery("SELECT id, delta FROM metrics").WillReturnRows(rows)

	got := repo.GetAllCounters(context.Background())
	if len(got) != 2 || got["PollCount"] != 5 || got["Errors"] != 10 {
		t.Fatalf("получили %v", got)
	}
}

func TestPostgresStorage_UpdateBatch_Empty(t *testing.T) {
	repo, _, cleanup := newMockRepo(t)
	defer cleanup()

	if err := repo.UpdateBatch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStorage_UpdateBatch(t *testing.T) {
	repo, mock, cleanup := newMockRepo(t)
	defer cleanup()

	v := 1.5
	d := int64(3)
	batch := []models.Metrics{
		{ID: "g1", MType: "gauge", Value: &v},
		{ID: "c1", MType: "counter", Delta: &d},
	}

	mock.ExpectBegin()
	mock.ExpectPrepare("INSERT INTO metrics")
	mock.ExpectExec("INSERT INTO metrics.*VALUES.*gauge").
		WithArgs("g1", 1.5).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO metrics.*VALUES.*counter").
		WithArgs("c1", int64(3)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := repo.UpdateBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

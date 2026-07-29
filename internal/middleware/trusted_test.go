package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedSubnetMiddleware(t *testing.T) {
	tests := []struct {
		name       string
		subnet     string
		header     string
		wantStatus int
	}{
		{"in_subnet", "192.168.1.0/24", "192.168.1.42", http.StatusOK},
		{"out_of_subnet", "192.168.1.0/24", "10.0.0.1", http.StatusForbidden},
		{"empty_header", "192.168.1.0/24", "", http.StatusForbidden},
		{"invalid_ip", "192.168.1.0/24", "not-an-ip", http.StatusForbidden},
		{"exact_ip", "10.0.0.5/32", "10.0.0.5", http.StatusOK},
		{"exact_ip_wrong", "10.0.0.5/32", "10.0.0.6", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw, err := TrustedSubnetMiddleware(tt.subnet)
			if err != nil {
				t.Fatal(err)
			}
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.header != "" {
				req.Header.Set("X-Real-IP", tt.header)
			}
			rec := httptest.NewRecorder()
			mw(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("статус: получили %d, ожидали %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestTrustedSubnetMiddleware_InvalidCIDR(t *testing.T) {
	if _, err := TrustedSubnetMiddleware("not-a-cidr"); err == nil {
		t.Fatal("ожидали ошибку на некорректном CIDR")
	}
}

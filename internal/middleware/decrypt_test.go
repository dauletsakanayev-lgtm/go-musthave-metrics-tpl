package middleware

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluegopher/go-musthave-metrics-tpl/internal/crypto"
)

func TestDecryptMiddleware_NilKey(t *testing.T) {
	mw := DecryptMiddleware(nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("plain")))
	req.Header.Set("X-Encrypted", "true")
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil-ключ должен пропускать, статус %d", rec.Code)
	}
	if rec.Body.String() != "plain" {
		t.Fatalf("тело не сохранилось: %q", rec.Body.String())
	}
}

func TestDecryptMiddleware_NoHeader(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	mw := DecryptMiddleware(priv)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	})
	// нет X-Encrypted → тело передаётся как есть
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("plain")))
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if rec.Body.String() != "plain" {
		t.Fatalf("тело должно быть без изменений: %q", rec.Body.String())
	}
}

func TestDecryptMiddleware_Success(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	original := []byte("hello secret")
	encrypted, err := crypto.Encrypt(&priv.PublicKey, original)
	if err != nil {
		t.Fatal(err)
	}

	mw := DecryptMiddleware(priv)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(encrypted))
	req.Header.Set("X-Encrypted", "true")
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("статус: %d, тело: %q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(original) {
		t.Fatalf("тело: получили %q, ожидали %q", rec.Body.String(), original)
	}
}

func TestDecryptMiddleware_BadCiphertext(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	mw := DecryptMiddleware(priv)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("garbage")))
	req.Header.Set("X-Encrypted", "true")
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ожидали 400, получили %d", rec.Code)
	}
}

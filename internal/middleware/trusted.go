package middleware

import (
	"net"
	"net/http"
	"strings"
)

// TrustedSubnetMiddleware возвращает middleware, пропускающий только
// те запросы, IP-адрес отправителя которых (заголовок X-Real-IP)
// входит в разрешённую CIDR-подсеть subnet. Возвращает 403 Forbidden,
// если заголовок отсутствует или IP не входит в подсеть.
//
// Формат subnet — CIDR ("192.168.1.0/24"). Некорректный CIDR
// приводит к возврату ненулевой err (обычно вызывающий код завершает
// сервис при таком раскладе).
//
// Если IP-подсеть не задана на уровне вызывающего кода, middleware
// подключать не следует — тогда никакой фильтрации не применяется.
func TrustedSubnetMiddleware(subnet string) (func(http.Handler) http.Handler, error) {
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, err
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := strings.TrimSpace(r.Header.Get("X-Real-IP"))
			if raw == "" {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
			ip := net.ParseIP(raw)
			if ip == nil || !ipNet.Contains(ip) {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

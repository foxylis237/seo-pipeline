// Package netbind уводит исходящие HTTP-соединения мимо VPN-туннеля.
//
// VPN-клиент с packet tunnel забирает маршрут по умолчанию и подменяет DNS на 198.18.0.0/15;
// obuchim-specialista.ru через его узел не проходит (TCP открывается, TLS не завершается),
// а через физический интерфейс отвечает. Правильная починка — правило самого VPN-клиента;
// пакет — запасной выход только для наших соединений, включается переменной окружения.
package netbind

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Interface — имя переменной окружения; значение — имя интерфейса («en0»), а не адрес:
// адрес меняется со сменой сети.
const Interface = "NETWORK_INTERFACE"

// Transport собирает транспорт, который открывает соединения с адреса названного интерфейса;
// пустое имя даёт nil (транспорт по умолчанию). Адрес ищется на каждом соединении: машину
// переносят между сетями.
func Transport(name string) (http.RoundTripper, error) {
	if name == "" {
		return nil, nil
	}
	if _, err := address(name); err != nil {
		// Опечатку в имени ловим на старте, а не на первом запросе.
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("http.DefaultTransport is %T, not *http.Transport", http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		local, err := address(name)
		if err != nil {
			return nil, err
		}
		bound := *dialer
		bound.LocalAddr = &net.TCPAddr{IP: local}
		return bound.DialContext(ctx, network, addr)
	}
	return transport, nil
}

// address возвращает IPv4-адрес интерфейса: привязка к IPv6 соединение мимо туннеля не уводит.
func address(name string) (net.IP, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("сетевой интерфейс %s (%s): %w", name, Interface, err)
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("адреса интерфейса %s: %w", name, err)
	}
	for _, addr := range addresses {
		network, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := network.IP.To4(); ip != nil && !ip.IsLoopback() {
			return ip, nil
		}
	}
	return nil, fmt.Errorf("у интерфейса %s нет адреса IPv4 — проверьте %s", name, Interface)
}

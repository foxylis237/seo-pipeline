// Package netbind уводит исходящие HTTP-соединения мимо VPN-туннеля.
//
// Зачем он есть. На рабочей машине стоит клиент VPN с packet tunnel: он забирает маршрут по
// умолчанию и подменяет DNS на диапазон 198.18.0.0/15. Часть площадок проекта через его узел
// не проходит вовсе — TCP открывается, TLS не завершается, — и все команды, которые ходят в
// блог, падают на пустом месте. Измерено 21.09.2026: obuchim-specialista.ru через туннель
// недоступен, через физический интерфейс отвечает 200; dpoprof.ru через тот же туннель ходит.
//
// Правильное место для починки — правило самого VPN-клиента («домен идёт напрямую»): оно чинит
// маршрут, а значит и браузер, и Playwright, и нас разом. Этот пакет — запасной выход для
// случая, когда до настроек клиента не дотянуться: он не трогает систему и действует только на
// те соединения, которые мы сами открываем.
//
// Поэтому он выключен по умолчанию и включается переменной окружения. Пустое значение —
// прежнее поведение до последнего байта: Dialer не подменяется, транспорт остаётся nil.
package netbind

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Interface — имя переменной окружения. Значение — имя сетевого интерфейса («en0»), а не
// адрес: адрес в Wi-Fi меняется, а имя интерфейса нет, и зашитый адрес протух бы молча при
// первой же смене сети.
const Interface = "NETWORK_INTERFACE"

// Transport собирает транспорт, который открывает соединения с адреса названного интерфейса.
//
// Пустое имя означает «ничего не делать» и даёт nil — его http.Client понимает как транспорт
// по умолчанию. Так вызывающему не нужно ветвиться: он передаёт результат как есть.
//
// Адрес интерфейса ищется на каждом соединении, а не один раз при сборке: машину переносят
// между сетями, и адрес, снятый при старте процесса, к середине прогона может уже не
// принадлежать интерфейсу.
func Transport(name string) (http.RoundTripper, error) {
	if name == "" {
		return nil, nil
	}
	if _, err := address(name); err != nil {
		// Проверка на старте, а не на первом запросе: опечатка в имени интерфейса обязана
		// стоить команды, а не двух часов прогона.
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
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

// address возвращает IPv4-адрес интерфейса.
//
// IPv6 не берётся намеренно: туннель раздаёт свои адреса обоим семействам, и привязка к
// IPv6-адресу физического интерфейса на этой машине соединение не уводит — проверено. Нужен
// ровно тот адрес, с которого отвечает `curl --interface`.
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

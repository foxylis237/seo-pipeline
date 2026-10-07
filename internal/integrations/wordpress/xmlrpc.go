package wordpress

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// xmlrpcPath — вход XML-RPC. REST принимает в meta только зарегистрированные ключи, полей ACF
// среди них нет; custom_fields у wp.newPost пишут произвольную postmeta, включая _yoast_wpseo_*.
const xmlrpcPath = "/xmlrpc.php"

// xmlrpcBlogID — единственный блог площадки (мультисайта нет).
const xmlrpcBlogID = 1

// maxXMLRPCResponseBytes ограничивает читаемый ответ; wp.getPost возвращает тело статьи целиком.
const maxXMLRPCResponseBytes = 16 << 20

// xmlrpcTimeout — бюджет одного вызова XML-RPC, независимый от Config.Timeout: на wp.newPost
// WordPress ещё перестраивает индексы Yoast и сбрасывает кэш.
const xmlrpcTimeout = 90 * time.Second

// mediaUploadTimeout — бюджет загрузки одного файла (по REST): время определяет ширина канала,
// а не WordPress.
const mediaUploadTimeout = 5 * time.Minute

// FaultError — отказ, о котором XML-RPC сообщил структурой fault.
type FaultError struct {
	Code    int
	Message string
}

func (e *FaultError) Error() string {
	return fmt.Sprintf("WordPress XML-RPC: fault %d: %s", e.Code, e.Message)
}

// xmlrpcMember — одно поле структуры XML-RPC; список, а не карта, ради стабильного порядка.
type xmlrpcMember struct {
	Name  string
	Value any
}

type xmlrpcStruct []xmlrpcMember

type xmlrpcArray []any

// call выполняет один вызов XML-RPC без повторов: безопасен ли повтор, знает только вызывающий.
func (c *Client) call(ctx context.Context, method string, params []any, out *xmlrpcResponse) error {
	body, err := encodeMethodCall(method, params)
	if err != nil {
		return fmt.Errorf("собрать вызов %s: %w", method, err)
	}
	endpoint := c.cfg.BaseURL + xmlrpcPath
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("собрать запрос %s: %w", method, err)
	}
	request.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	request.Header.Set("Accept", "text/xml")
	// Пароль уходит внутри тела вызова, поэтому тело не логируется.

	response, err := c.xmlrpcClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("вызов %s прерван: %w", method, ctxErr)
		}
		return &transportError{Endpoint: xmlrpcPath, Err: err}
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxXMLRPCResponseBytes))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("прочитать ответ %s: %w", method, ctxErr)
		}
		return &transportError{Endpoint: xmlrpcPath, Err: fmt.Errorf("прочитать ответ: %w", err)}
	}
	if response.StatusCode != http.StatusOK {
		// XML-RPC отвечает 200 и на ошибку (fault); другой код — запрос не дошёл до обработчика.
		return newStatusError(xmlrpcPath, response, raw, c.cfg.AppPassword)
	}
	decoded, err := decodeMethodResponse(raw)
	if err != nil {
		return fmt.Errorf("разобрать ответ %s: %w", method, err)
	}
	if decoded.Fault != nil {
		decoded.Fault.Message = truncate(redactSecret(decoded.Fault.Message, c.cfg.AppPassword), messageLimit)
		return decoded.Fault
	}
	*out = decoded
	return nil
}

type xmlrpcResponse struct {
	Value any
	Fault *FaultError
}

// encodeMethodCall собирает тело вызова; своя сборка вместо библиотеки — типов значений немного.
func encodeMethodCall(method string, params []any) ([]byte, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><methodCall><methodName>`)
	if err := xml.EscapeText(&b, []byte(method)); err != nil {
		return nil, err
	}
	b.WriteString(`</methodName><params>`)
	for _, param := range params {
		b.WriteString(`<param>`)
		if err := encodeValue(&b, param); err != nil {
			return nil, err
		}
		b.WriteString(`</param>`)
	}
	b.WriteString(`</params></methodCall>`)
	return []byte(b.String()), nil
}

// encodeValue кодирует значение; неизвестный тип — ошибка, а не приведение к строке.
func encodeValue(b *strings.Builder, value any) error {
	b.WriteString(`<value>`)
	defer b.WriteString(`</value>`)

	switch typed := value.(type) {
	case string:
		b.WriteString(`<string>`)
		if err := escapeXMLRPCText(b, typed); err != nil {
			return err
		}
		b.WriteString(`</string>`)
	case int:
		fmt.Fprintf(b, `<int>%d</int>`, typed)
	case int64:
		fmt.Fprintf(b, `<int>%d</int>`, typed)
	case bool:
		digit := 0
		if typed {
			digit = 1
		}
		fmt.Fprintf(b, `<boolean>%d</boolean>`, digit)
	case xmlrpcStruct:
		b.WriteString(`<struct>`)
		for _, member := range typed {
			b.WriteString(`<member><name>`)
			if err := xml.EscapeText(b, []byte(member.Name)); err != nil {
				return err
			}
			b.WriteString(`</name>`)
			if err := encodeValue(b, member.Value); err != nil {
				return err
			}
			b.WriteString(`</member>`)
		}
		b.WriteString(`</struct>`)
	case xmlrpcArray:
		b.WriteString(`<array><data>`)
		for _, item := range typed {
			if err := encodeValue(b, item); err != nil {
				return err
			}
		}
		b.WriteString(`</data></array>`)
	default:
		return fmt.Errorf("XML-RPC: unsupported value type %T", value)
	}
	return nil
}

// escapeXMLRPCText экранирует текст и выбрасывает символы, недопустимые в XML 1.0:
// один такой символ делает весь запрос неразбираемым для WordPress.
func escapeXMLRPCText(b *strings.Builder, value string) error {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return r
		case r < 0x20:
			return -1
		case r >= 0xD800 && r <= 0xDFFF:
			return -1
		case r == 0xFFFE || r == 0xFFFF:
			return -1
		default:
			return r
		}
	}, value)
	// \n уходит как &#xA;, WordPress возвращает его переводом строки.
	return xml.EscapeText(b, []byte(cleaned))
}

// decodeMethodResponse разбирает ответ по токенам: значение XML-RPC рекурсивно и полиморфно,
// тегами структуры его не описать.
func decodeMethodResponse(raw []byte) (xmlrpcResponse, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return xmlrpcResponse{}, errors.New("XML-RPC: в ответе нет ни params, ни fault")
		}
		if err != nil {
			return xmlrpcResponse{}, err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "fault":
			value, err := decodeNextValue(decoder)
			if err != nil {
				return xmlrpcResponse{}, err
			}
			return xmlrpcResponse{Fault: faultFromValue(value)}, nil
		case "params":
			value, err := decodeNextValue(decoder)
			if err != nil {
				return xmlrpcResponse{}, err
			}
			return xmlrpcResponse{Value: value}, nil
		}
	}
}

func faultFromValue(value any) *FaultError {
	fault := &FaultError{Code: -1, Message: "неизвестный отказ"}
	members, ok := value.(map[string]any)
	if !ok {
		return fault
	}
	if code, ok := members["faultCode"]; ok {
		fault.Code = intFromValue(code)
	}
	if message, ok := members["faultString"].(string); ok && message != "" {
		fault.Message = message
	}
	return fault
}

// decodeNextValue находит ближайший <value> и разбирает его целиком.
func decodeNextValue(decoder *xml.Decoder) (any, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "value" {
			return decodeValue(decoder)
		}
	}
}

// decodeValue разбирает содержимое уже открытого <value>; без вложенного тега это строка.
func decodeValue(decoder *xml.Decoder) (any, error) {
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.CharData:
			text.Write(typed)
		case xml.StartElement:
			switch typed.Name.Local {
			case "string":
				return decodeText(decoder)
			case "int", "i4", "i8":
				raw, err := decodeText(decoder)
				if err != nil {
					return nil, err
				}
				number, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if err != nil {
					return nil, fmt.Errorf("XML-RPC: неразборчивое число %q", raw)
				}
				return number, nil
			case "boolean":
				raw, err := decodeText(decoder)
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(raw) == "1", nil
			case "double", "dateTime.iso8601", "base64":
				// Не нужны пакету; строкой, чтобы не уронить разбор соседних полей.
				return decodeText(decoder)
			case "array":
				return decodeArray(decoder)
			case "struct":
				return decodeStruct(decoder)
			case "nil":
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
				return nil, nil
			default:
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			return text.String(), nil
		}
	}
}

// decodeText собирает текст открытого элемента и закрывает его.
func decodeText(decoder *xml.Decoder) (string, error) {
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", err
		}
		switch typed := token.(type) {
		case xml.CharData:
			text.Write(typed)
		case xml.EndElement:
			return text.String(), nil
		case xml.StartElement:
			if err := decoder.Skip(); err != nil {
				return "", err
			}
		}
	}
}

func decodeArray(decoder *xml.Decoder) ([]any, error) {
	items := make([]any, 0)
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "data":
			case "value":
				item, err := decodeValue(decoder)
				if err != nil {
					return nil, err
				}
				items = append(items, item)
			default:
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if typed.Name.Local == "array" {
				return items, nil
			}
		}
	}
}

func decodeStruct(decoder *xml.Decoder) (map[string]any, error) {
	members := make(map[string]any)
	name := ""
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "member":
				name = ""
			case "name":
				raw, err := decodeText(decoder)
				if err != nil {
					return nil, err
				}
				name = strings.TrimSpace(raw)
			case "value":
				value, err := decodeValue(decoder)
				if err != nil {
					return nil, err
				}
				if name != "" {
					members[name] = value
				}
			default:
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if typed.Name.Local == "struct" {
				return members, nil
			}
		}
	}
}

// intFromValue приводит к числу то, что WordPress отдаёт то числом, то строкой (id от wp.newPost).
func intFromValue(value any) int {
	switch typed := value.(type) {
	case int64:
		return int(typed)
	case string:
		number, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return -1
		}
		return number
	default:
		return -1
	}
}

func stringFromValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		if typed {
			return "1"
		}
		return "0"
	default:
		return ""
	}
}

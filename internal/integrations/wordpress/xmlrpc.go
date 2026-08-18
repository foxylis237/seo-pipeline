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

// xmlrpcPath — второй и последний вход в WordPress, которым пользуется пакет.
//
// Он существует не по любви к XML-RPC, а по единственной причине: REST этого сайта принимает
// в meta только зарегистрированные ключи, а полей ACF среди них нет. wp.newPost принимает
// custom_fields и пишет произвольную postmeta — другого способа заполнить ACF, не меняя
// ничего на стороне WordPress, нет. Проверено на живой площадке: черновик со всеми 19
// ключами лёг побайтово, включая защищённые _yoast_wpseo_*.
//
// Чтение остаётся на REST: там уже есть повторы, классификация отказов и вычистка секрета.
const xmlrpcPath = "/xmlrpc.php"

// xmlrpcBlogID — единственный блог площадки. Мультисайта у проекта нет и не планируется,
// поэтому значение зафиксировано здесь, а не протекает в конфигурацию.
const xmlrpcBlogID = 1

// maxXMLRPCResponseBytes ограничивает читаемый ответ. Тело статьи возвращается целиком в
// wp.getPost при обратной сверке, поэтому лимит заметно выше REST-ного.
const maxXMLRPCResponseBytes = 16 << 20

// xmlrpcTimeout — бюджет одного вызова XML-RPC.
//
// Он не выводится из Config.Timeout и не складывается с ним: тело статьи — десятки килобайт,
// а WordPress на wp.newPost успевает сходить в базу, перестроить индексы Yoast и сбросить
// кэш. Десяти секунд чтения справочника здесь мало, а перемножать вложенные таймауты в этом
// проекте уже дорого обходилось (ловушка H6).
//
// Повтором истёкший бюджет не лечится: у записи повторов нет.
const xmlrpcTimeout = 90 * time.Second

// mediaUploadTimeout — бюджет загрузки одного файла в медиабиблиотеку.
//
// Живёт рядом с бюджетом XML-RPC намеренно: оба относятся к записывающим вызовам, и видеть
// их надо вместе. Сама загрузка идёт по REST — там у неё первоклассные поля alt и title.
//
// Отдельный от xmlrpcTimeout и заметно больший: у обложки статьи вес измеряется мегабайтами,
// и время запроса определяется шириной канала, а не работой WordPress. Девяноста секунд
// wp.newPost хватает с запасом, а двадцатимегабайтному webp на медленном аплинке — нет.
//
// Повторов у загрузки нет, как и у создания записи: истёкший бюджет означает, что файл мог
// уже лечь в библиотеку, и вторая попытка положила бы туда его копию.
const mediaUploadTimeout = 5 * time.Minute

// FaultError — отказ, о котором XML-RPC сообщил структурой fault.
//
// Отдельный тип, потому что классифицировать его надо через errors.As, как и StatusError:
// текст сообщения зависит от локали сайта и набора плагинов, а код — нет.
type FaultError struct {
	Code    int
	Message string
}

func (e *FaultError) Error() string {
	return fmt.Sprintf("WordPress XML-RPC: fault %d: %s", e.Code, e.Message)
}

// xmlrpcMember — одно поле структуры XML-RPC. Порядок сохраняется списком, а не картой:
// иначе один и тот же запрос сериализовался бы каждый раз по-новому, и сравнить его в тесте
// было бы нечем.
type xmlrpcMember struct {
	Name  string
	Value any
}

type xmlrpcStruct []xmlrpcMember

type xmlrpcArray []any

// call выполняет один вызов XML-RPC. Повторов здесь нет и быть не может.
//
// Метод общий для чтения и записи, но политика повторов — нет: решать, безопасен ли повтор,
// имеет право только вызывающий, который знает, создаёт его метод запись или нет. Для
// wp.newPost повтор запрещён при любом отказе: неизвестно, дошёл ли первый запрос, а вторая
// попытка — это второй пост в блоге, удалить который мы не можем.
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
	// Пароль уходит внутри тела вызова, как того требует протокол. Тело не логируется нигде
	// и ни при каком уровне логов — по той же причине, по которой не логируется заголовок
	// Authorization у REST.

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
		// XML-RPC отвечает 200 даже на ошибку — она приезжает структурой fault. Любой другой
		// код означает, что до обработчика запрос не дошёл: выключенный xmlrpc.php, плагин
		// безопасности, заглушка хостера.
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

// encodeMethodCall собирает тело вызова.
//
// Своя сборка, а не сторонняя библиотека: пакету нужны ровно два метода и четыре типа
// значений, а зависимость пришлось бы обновлять и проверять ради этого объёма.
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

// encodeValue поддерживает ровно те типы, которые встречаются в наших вызовах.
// Неизвестный тип — ошибка, а не тихое приведение к строке: молча отправленное не то
// значение обнаружилось бы уже опубликованной записью.
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

// escapeXMLRPCText экранирует текст и выбрасывает символы, которых в XML 1.0 быть не может.
//
// Выбрасывание нужно телу статьи: HTML приходит из внешнего источника, и один управляющий
// символ сделал бы весь запрос неразбираемым для WordPress. Перевод строки, возврат каретки
// и табуляция допустимы и сохраняются.
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
	// xml.EscapeText переводит \n в &#xA;, и WordPress возвращает его обратно переводом
	// строки — обратная сверка после создания это подтверждает.
	return xml.EscapeText(b, []byte(cleaned))
}

// decodeMethodResponse разбирает ответ в обобщённое значение.
//
// Разбор идёт по токенам, а не через структуры encoding/xml: у XML-RPC значение
// рекурсивно и полиморфно, и описать его тегами структуры нельзя.
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

// decodeValue разбирает содержимое уже открытого <value>.
//
// Отсутствие вложенного тега — это <string> по спецификации, и WordPress этим пользуется.
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
				// Ни одно из наших полей этих типов не имеет. Значение сохраняется строкой,
				// чтобы разбор соседних полей не рухнул из-за поля, которое нам не нужно.
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
			// </value> без вложенного тега — строка.
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
				// Обёртка без собственного значения — просто идём дальше.
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

// intFromValue приводит к числу то, что WordPress отдаёт то числом, то строкой.
// wp.newPost, например, возвращает идентификатор новой записи строкой.
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

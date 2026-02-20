package jsonrpc

import (
	"encoding/json"
)

const Version = "2.0"

const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// Request is a JSON-RPC 2.0 request/notification.
// HasID distinguishes notifications (no id field) from requests with id: null.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id,omitempty"`

	hasID bool
}

func (r *Request) UnmarshalJSON(data []byte) error {
	type alias struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
		ID      any             `json:"id"`
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}

	r.JSONRPC = a.JSONRPC
	r.Method = a.Method
	r.Params = a.Params
	r.ID = a.ID
	_, r.hasID = raw["id"]

	return nil
}

func (r Request) IsNotification() bool {
	return !r.hasID
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

func Success(id any, result any) Response {
	return Response{JSONRPC: Version, ID: id, Result: result}
}

func Failure(id any, code int, message string, data any) Response {
	return Response{
		JSONRPC: Version,
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

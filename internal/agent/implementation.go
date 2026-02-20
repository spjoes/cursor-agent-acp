package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/spjoes/cursor-agent-acp/internal/acp"
	"github.com/spjoes/cursor-agent-acp/internal/jsonrpc"
	"github.com/spjoes/cursor-agent-acp/internal/logging"
	"github.com/spjoes/cursor-agent-acp/internal/server"
)

type Implementation struct {
	server *server.Server
	logger *logging.Logger

	reqID uint64
}

func NewImplementation(serverInstance *server.Server, logger *logging.Logger) *Implementation {
	return &Implementation{
		server: serverInstance,
		logger: logger,
	}
}

func (i *Implementation) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	result, err := i.call(ctx, "initialize", params, false)
	if err != nil {
		return acp.InitializeResponse{}, err
	}
	return decodeResult[acp.InitializeResponse](result)
}

func (i *Implementation) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	result, err := i.call(ctx, "session/new", params, false)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	return decodeResult[acp.NewSessionResponse](result)
}

func (i *Implementation) LoadSession(ctx context.Context, params acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	result, err := i.call(ctx, "session/load", params, false)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	return decodeResult[acp.LoadSessionResponse](result)
}

func (i *Implementation) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	result, err := i.call(ctx, "session/set_mode", params, false)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return decodeResult[acp.SetSessionModeResponse](result)
}

func (i *Implementation) SetSessionModel(ctx context.Context, params acp.SetSessionModelRequest) (acp.SetSessionModelResponse, error) {
	result, err := i.call(ctx, "session/set_model", params, false)
	if err != nil {
		return acp.SetSessionModelResponse{}, err
	}
	return decodeResult[acp.SetSessionModelResponse](result)
}

func (i *Implementation) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	result, err := i.call(ctx, "session/prompt", params, false)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	return decodeResult[acp.PromptResponse](result)
}

func (i *Implementation) Cancel(ctx context.Context, params acp.CancelNotification) error {
	_, err := i.call(ctx, "session/cancel", params, true)
	return err
}

func (i *Implementation) Authenticate(_ context.Context, _ map[string]any) (map[string]any, error) {
	// The Go adapter does not require authentication beyond cursor-agent CLI auth.
	return map[string]any{}, nil
}

func (i *Implementation) ExtMethod(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	result, err := i.call(ctx, method, params, false)
	if err != nil {
		return nil, err
	}
	return decodeResult[map[string]any](result)
}

func (i *Implementation) ExtNotification(ctx context.Context, method string, params map[string]any) error {
	_, err := i.call(ctx, method, params, true)
	return err
}

func (i *Implementation) call(ctx context.Context, method string, params any, notification bool) (any, error) {
	req, err := i.newRequest(method, params, !notification)
	if err != nil {
		return nil, err
	}
	resp := i.server.ProcessRequest(ctx, req)
	if resp.Error != nil {
		return nil, fmt.Errorf("%s (code=%d)", resp.Error.Message, resp.Error.Code)
	}
	return resp.Result, nil
}

func (i *Implementation) newRequest(method string, params any, includeID bool) (jsonrpc.Request, error) {
	requestMap := map[string]any{
		"jsonrpc": jsonrpc.Version,
		"method":  method,
		"params":  params,
	}
	if includeID {
		id := atomic.AddUint64(&i.reqID, 1)
		requestMap["id"] = strconv.FormatUint(id, 10)
	}

	raw, err := json.Marshal(requestMap)
	if err != nil {
		return jsonrpc.Request{}, err
	}
	var req jsonrpc.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonrpc.Request{}, err
	}
	return req, nil
}

func decodeResult[T any](result any) (T, error) {
	var out T
	raw, err := json.Marshal(result)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

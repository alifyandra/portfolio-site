// Package mcp is the remote Model Context Protocol server for the finance ledger
// (ADR 0017). It exposes the read side of the ledger as MCP tools, so an MCP client
// (Claude, an editor, a CLI) can ask about net worth, accounts, transactions and
// spend without a browser session.
//
// It is a hand-rolled, spec-correct JSON-RPC 2.0 handler over the MCP Streamable
// HTTP transport in JSON-response mode: a client POSTs one JSON-RPC request and gets
// one application/json response. No SSE, no long-lived stream, no session id, so the
// endpoint is an ordinary request/response that a Cloudflare-proxied origin serves
// unchanged. The official Go SDK was considered but its Streamable HTTP handler is
// built around an SSE-capable session model; for a stateless read-only surface a
// small hand-rolled handler is fewer moving parts and keeps the dependency footprint
// at zero (see ADR 0017 for the trade-off).
//
// It is mounted on the RAW Chi router (not Huma) because it does not speak the
// OpenAPI contract and authenticates with a scope-only bearer token (finance.read),
// never the session cookie. Constructing the handler dereferences nothing, so
// cmd/spec (nil Ent/Auth) stays safe; the clients are touched only per request.
package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

const (
	// financeReadScope is the bearer scope a token must carry to reach any MCP tool.
	// It is a distinct scope from finance.sync (the write/ingest side): a read token
	// can never ingest, and an ingest token can never read.
	financeReadScope = "finance.read"

	serverName    = "aliflabs-finance"
	serverVersion = "0.1.0"

	// latestProtocolVersion is the MCP revision this server implements. On initialize
	// it echoes the client's requested version when we also support it, else it offers
	// this one.
	latestProtocolVersion = "2025-06-18"

	// maxRequestBytes bounds a single JSON-RPC request body. Tool arguments are tiny;
	// this just stops a malicious client from streaming an unbounded body at the box.
	maxRequestBytes = 1 << 20 // 1 MiB
)

// supportedProtocolVersions are the MCP revisions this server can speak, newest first.
var supportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC 2.0 error codes (subset used here).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Deps are the dependencies the MCP server needs. Both may be nil at construction
// (cmd/spec builds the router with nil clients); they are only used per request.
type Deps struct {
	Ent  *ent.Client
	Auth *auth.Service
}

// server is the http.Handler mounted at /mcp.
type server struct {
	deps Deps
}

// Handler builds the MCP http.Handler. It dereferences nothing, so it is safe to
// mount even when Ent/Auth are nil (spec generation).
func Handler(deps Deps) http.Handler {
	return &server{deps: deps}
}

// rpcRequest is a JSON-RPC 2.0 request or notification. A notification omits id.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response. Exactly one of Result / Error is set.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func resultResponse(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// ServeHTTP implements the Streamable HTTP transport in JSON-response mode. Only POST
// carries JSON-RPC; GET/DELETE (the SSE/session verbs) are refused with 405 since we
// offer no stream. Auth runs before any parsing, so an unauthenticated request never
// reaches the JSON-RPC layer.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "only POST is supported (JSON-RPC over Streamable HTTP, JSON mode)", http.StatusMethodNotAllowed)
		return
	}

	if _, ok := s.authorize(r); !ok {
		// A single WWW-Authenticate challenge for every auth failure (missing token,
		// invalid token, or a token that lacks finance.read): the client cannot tell
		// which, by design.
		w.Header().Set("WWW-Authenticate", `Bearer realm="mcp", scope="`+financeReadScope+`"`)
		http.Error(w, "a valid finance.read bearer token is required", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		writeJSON(w, http.StatusOK, errorResponse(nil, codeParseError, "failed to read request body"))
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusOK, errorResponse(nil, codeParseError, "parse error: request is not valid JSON"))
		return
	}
	if req.JSONRPC != "2.0" {
		writeJSON(w, http.StatusOK, errorResponse(req.ID, codeInvalidRequest, `invalid request: "jsonrpc" must be "2.0"`))
		return
	}

	// A notification (no id) gets no response body: process any side effect and
	// acknowledge with 202. notifications/initialized is the only one we expect.
	isNotification := len(req.ID) == 0

	resp := s.dispatch(r.Context(), &req)

	if isNotification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// dispatch routes a single JSON-RPC method to its handler.
func (s *server) dispatch(ctx context.Context, req *rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized":
		// A notification; ServeHTTP swallows the (empty) response.
		return rpcResponse{}
	case "ping":
		return resultResponse(req.ID, map[string]any{})
	case "tools/list":
		return resultResponse(req.ID, map[string]any{"tools": toolDefinitions()})
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return errorResponse(req.ID, codeMethodNotFound, "method not found: "+req.Method)
	}
}

// initializeParams is the subset of the initialize params we read: the client's
// requested protocol version, which we echo back when we support it.
type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// handleInitialize negotiates the protocol version and advertises the tools
// capability + server identity.
func (s *server) handleInitialize(req *rpcRequest) rpcResponse {
	var p initializeParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p) // best-effort; an unreadable params just defaults the version
	}
	version := negotiateVersion(p.ProtocolVersion)
	return resultResponse(req.ID, map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
	})
}

// negotiateVersion echoes the client's requested version when it is one we support,
// otherwise offers our latest. Per the MCP spec the client may then disconnect if it
// cannot speak the offered version.
func negotiateVersion(requested string) string {
	for _, v := range supportedProtocolVersions {
		if v == requested {
			return requested
		}
	}
	return latestProtocolVersion
}

// authorize resolves the Authorization: Bearer token and requires the finance.read
// scope. It returns (identity, true) only for a valid, sufficiently-scoped token; any
// failure (no Auth service, missing/invalid token, or insufficient scope) is
// (nil, false), which ServeHTTP renders as a single 401 challenge. It never accepts a
// session cookie: MCP is token-only.
func (s *server) authorize(r *http.Request) (*auth.BearerIdentity, bool) {
	if s.deps.Auth == nil {
		return nil, false
	}
	raw := bearerToken(r.Header.Get("Authorization"))
	if raw == "" {
		return nil, false
	}
	id, err := s.deps.Auth.AuthenticateBearer(r.Context(), raw)
	if err != nil || id == nil {
		return nil, false
	}
	if !id.Allows(financeReadScope) {
		return nil, false
	}
	return id, true
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header, or
// "" when absent or not a Bearer scheme (case-insensitive per RFC 6750).
func bearerToken(h string) string {
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// writeJSON encodes v as an application/json response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

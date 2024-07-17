package opsimulator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	ophttp "github.com/ethereum-optimism/optimism/op-service/httputil"
	"github.com/ethereum-optimism/supersim/chainapi"
	"github.com/ethereum/go-ethereum/log"
)

const (
	host = "127.0.0.1"
)

type Config struct {
	Port          uint64
	SourceChainID uint64
}

type OpSimulator struct {
	log        log.Logger
	l1Chain    chainapi.Chain
	l2Chain    chainapi.Chain
	httpServer *ophttp.HTTPServer

	stopped atomic.Bool

	cfg *Config
}

type JSONRPCRequest struct {
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
	JSONRPC string        `json:"jsonrpc"`
}

func New(log log.Logger, cfg *Config, l1Chain chainapi.Chain, l2Chain chainapi.Chain) *OpSimulator {
	return &OpSimulator{
		log:     log,
		cfg:     cfg,
		l1Chain: l1Chain,
		l2Chain: l2Chain,
	}
}

func (opSim *OpSimulator) Start(ctx context.Context) error {
	proxy, err := opSim.createReverseProxy()
	if err != nil {
		return fmt.Errorf("error creating reverse proxy: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", handler(proxy))

	hs, err := ophttp.StartHTTPServer(net.JoinHostPort(host, fmt.Sprintf("%d", opSim.cfg.Port)), mux)
	if err != nil {
		return fmt.Errorf("failed to start HTTP RPC server: %w", err)
	}

	opSim.log.Info("started opsimulator", "chain.id", opSim.ChainID(), "addr", hs.Addr())
	opSim.httpServer = hs

	if opSim.cfg.Port == 0 {
		port, err := strconv.ParseInt(strings.Split(hs.Addr().String(), ":")[1], 10, 64)
		if err != nil {
			panic(fmt.Errorf("unexpected opsimulator listening port: %w", err))
		}

		opSim.cfg.Port = uint64(port)
	}

	return nil
}

func (opSim *OpSimulator) Stop(ctx context.Context) error {
	if opSim.stopped.Load() {
		return errors.New("already stopped")
	}
	if !opSim.stopped.CompareAndSwap(false, true) {
		return nil // someone else stopped
	}

	return opSim.httpServer.Stop(ctx)
}

func (a *OpSimulator) Stopped() bool {
	return a.stopped.Load()
}

func handler(proxy *httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusInternalServerError)
			return
		}
		defer r.Body.Close()

		var req JSONRPCRequest
		err = json.Unmarshal(body, &req)
		if err != nil {
			http.Error(w, "Failed to parse JSON-RPC request", http.StatusBadRequest)
			return
		}

		// TODO(https://github.com/ethereum-optimism/supersim/issues/55): support batch txs

		if req.Method == "eth_sendRawTransaction" {
			checkInteropInvariants()
		}

		r.Body = io.NopCloser(bytes.NewReader(body))

		proxy.ServeHTTP(w, r)
	}
}

// TODO(https://github.com/ethereum-optimism/supersim/issues/19): add logic for checking that an interop transaction is valid.
func checkInteropInvariants() bool {
	return true
}

func (opSim *OpSimulator) createReverseProxy() (*httputil.ReverseProxy, error) {
	targetURL, err := url.Parse(opSim.l2Chain.Endpoint())
	if err != nil {
		return nil, fmt.Errorf("failed to parse target URL: %w", err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(targetURL)
		},
	}
	return proxy, nil
}

func (opSim *OpSimulator) Endpoint() string {
	return fmt.Sprintf("http://%s:%d", host, opSim.cfg.Port)
}

func (opSim *OpSimulator) ChainID() uint64 {
	return opSim.l2Chain.ChainID()
}

func (opSim *OpSimulator) SourceChainID() uint64 {
	return opSim.cfg.SourceChainID
}

func (opSim *OpSimulator) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Chain ID: %d    RPC: %s    LogPath: %s", opSim.ChainID(), opSim.Endpoint(), opSim.l2Chain.LogPath())
	return b.String()
}

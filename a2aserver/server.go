package a2aserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2acompat/a2av0"
	a2agrpcv0 "github.com/a2aproject/a2a-go/v2/a2agrpc/v0"
	a2agrpcv1 "github.com/a2aproject/a2a-go/v2/a2agrpc/v1"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/limiter"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/speed2exe/ag/app"
	"github.com/speed2exe/ag/log"
	"google.golang.org/grpc"
)

const (
	legacyAgentCardPath = "/.well-known/agent.json"
	shutdownTimeout     = 10 * time.Second
)

// Server runs an A2A-compliant server backed by an ag Runtime.
type Server struct {
	runtime      *app.Runtime
	publicCard   *a2a.AgentCard
	extendedCard *a2a.AgentCard
	httpBaseURL  string
	grpcTarget   string

	httpListener net.Listener
	httpServer   *http.Server
	grpcListener net.Listener
	grpcServer   *grpc.Server

	handler    a2asrv.RequestHandler
	taskStore  *FileTaskStore
	pushStore  *FilePushConfigStore
	contextMap *ContextMapStore

	closeOnce sync.Once
}

// New initializes persistent stores, binds network listeners, constructs the
// AgentCard(s), and wires all A2A v1.0 (and optional v0.3 compat / gRPC) handlers.
func New(rt *app.Runtime) (*Server, error) {
	if rt == nil || rt.Settings == nil {
		return nil, errors.New("runtime and settings are required")
	}
	s := rt.Settings

	taskStore, err := NewFileTaskStore(filepath.Join(s.A2aServerDataDir, "tasks"), &FileTaskStoreConfig{
		Authenticator: a2asrv.NewTaskStoreAuthenticator(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize A2A task store: %w", err)
	}
	pushStore, err := NewFilePushConfigStore(filepath.Join(s.A2aServerDataDir, "push_configs"))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize A2A push config store: %w", err)
	}
	contextMap, err := NewContextMapStore(filepath.Join(s.A2aServerDataDir, "contexts.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize A2A context store: %w", err)
	}

	httpLn, err := net.Listen("tcp", s.A2aServerAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on HTTP address %q: %w", s.A2aServerAddr, err)
	}
	var grpcLn net.Listener
	if s.A2aServerGrpcAddr != "" {
		grpcLn, err = net.Listen("tcp", s.A2aServerGrpcAddr)
		if err != nil {
			_ = httpLn.Close()
			return nil, fmt.Errorf("failed to listen on gRPC address %q: %w", s.A2aServerGrpcAddr, err)
		}
	}

	cleanupListeners := func() {
		_ = httpLn.Close()
		if grpcLn != nil {
			_ = grpcLn.Close()
		}
	}

	httpBaseURL := s.A2aServerPublicURL
	if httpBaseURL == "" {
		httpBaseURL = defaultHTTPURL(httpLn.Addr().String())
	}
	grpcTarget := s.A2aServerGrpcURL
	if grpcTarget == "" && grpcLn != nil {
		grpcTarget = defaultGRPCURL(grpcLn.Addr().String())
	}

	authCfg := AuthConfig{
		BearerToken:  s.A2aServerBearerToken,
		APIKey:       s.A2aServerApiKey,
		APIKeyHeader: s.A2aServerApiKeyHeader,
	}

	executor := NewAgentExecutor(rt, contextMap, s.A2aServerOutputModes)
	publicCard, extendedCard, err := BuildAgentCards(s, rt.Skills, executor.Tools(), httpBaseURL, grpcTarget, authCfg)
	if err != nil {
		cleanupListeners()
		return nil, fmt.Errorf("failed to build agent card: %w", err)
	}

	// Keep executor's negotiated output modes aligned with the final card
	// (including any --a2a-card-path override).
	if len(publicCard.DefaultOutputModes) > 0 {
		executor = NewAgentExecutor(rt, contextMap, publicCard.DefaultOutputModes)
	}

	cardEnforcer, err := a2asrv.NewAgentCardEnforcer(publicCard)
	if err != nil {
		cleanupListeners()
		return nil, fmt.Errorf("failed to build agent card enforcer: %w", err)
	}

	interceptors := []a2asrv.CallInterceptor{
		a2asrv.NewErrorSanitizer(),
	}
	if authCfg.Enabled() {
		interceptors = append(interceptors, NewAuthInterceptor(authCfg))
	}
	interceptors = append(interceptors, cardEnforcer)

	panicHandler := func(r any) error {
		log.Errorf("a2aserver: recovered panic: %v\n%s", r, string(debug.Stack()))
		return a2a.ErrInternalError
	}

	pushSender := push.NewHTTPPushSender(&push.HTTPSenderConfig{
		AllowPrivateNetworks: s.A2aServerAllowPrivatePush,
	})

	handlerOpts := []a2asrv.RequestHandlerOption{
		a2asrv.WithLogger(slog.Default()),
		a2asrv.WithTaskStore(taskStore),
		a2asrv.WithPushNotifications(pushStore, pushSender),
		a2asrv.WithExecutorContextInterceptor(&a2asrv.ReferencedTasksLoader{Store: taskStore}),
		a2asrv.WithExecutionPanicHandler(panicHandler),
		a2asrv.WithCallInterceptors(interceptors...),
	}
	if extendedCard != nil {
		handlerOpts = append(handlerOpts, a2asrv.WithExtendedAgentCard(extendedCard))
	}
	if s.A2aServerInactivityTimeout > 0 {
		handlerOpts = append(handlerOpts, a2asrv.WithAgentInactivityTimeout(s.A2aServerInactivityTimeout))
	}
	if s.A2aServerMaxConcurrent > 0 {
		handlerOpts = append(handlerOpts, a2asrv.WithConcurrencyConfig(limiter.ConcurrencyConfig{
			MaxExecutions: s.A2aServerMaxConcurrent,
		}))
	}

	reqHandler := a2asrv.NewHandler(executor, handlerOpts...)

	transportOpts := []a2asrv.TransportOption{
		a2asrv.WithTransportPanicHandler(panicHandler),
	}
	if s.A2aServerKeepAlive > 0 {
		transportOpts = append(transportOpts, a2asrv.WithTransportKeepAlive(s.A2aServerKeepAlive))
	}

	httpMux := buildHTTPMux(publicCard, reqHandler, s.A2aServerCompatV0, transportOpts...)
	httpSrv := &http.Server{
		Handler:           httpMux,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout is intentionally 0 so SSE streams are not terminated.
	}

	var grpcSrv *grpc.Server
	if grpcLn != nil {
		grpcSrv = grpc.NewServer()
		a2agrpcv1.NewHandler(reqHandler).RegisterWith(grpcSrv)
		if s.A2aServerCompatV0 {
			a2agrpcv0.NewHandler(reqHandler).RegisterWith(grpcSrv)
		}
	}

	return &Server{
		runtime:      rt,
		publicCard:   publicCard,
		extendedCard: extendedCard,
		httpBaseURL:  httpBaseURL,
		grpcTarget:   grpcTarget,
		httpListener: httpLn,
		httpServer:   httpSrv,
		grpcListener: grpcLn,
		grpcServer:   grpcSrv,
		handler:      reqHandler,
		taskStore:    taskStore,
		pushStore:    pushStore,
		contextMap:   contextMap,
	}, nil
}

func buildHTTPMux(
	publicCard *a2a.AgentCard,
	reqHandler a2asrv.RequestHandler,
	compatV0 bool,
	transportOpts ...a2asrv.TransportOption,
) *http.ServeMux {
	mux := http.NewServeMux()

	var cardHandler http.Handler
	if compatV0 {
		cardHandler = a2asrv.NewAgentCardHandler(a2av0.NewStaticAgentCardProducer(publicCard))
	} else {
		cardHandler = a2asrv.NewStaticAgentCardHandler(publicCard)
	}
	mux.Handle(a2asrv.WellKnownAgentCardPath, cardHandler)

	v1JSONRPC := a2asrv.NewJSONRPCHandler(reqHandler, transportOpts...)
	v1REST := a2asrv.NewRESTHandler(reqHandler, transportOpts...)

	mux.Handle("/jsonrpc", v1JSONRPC)
	// Also accept JSON-RPC POSTs at root "/" for clients expecting the canonical root URL.
	mux.Handle("POST /{$}", v1JSONRPC)

	if compatV0 {
		mux.Handle(legacyAgentCardPath, cardHandler)
		mux.Handle("/v0/jsonrpc", a2av0.NewJSONRPCHandler(reqHandler, transportOpts...))
		// a2av0.NewRESTHandler mounts its routes under /v1/* directly.
		mux.Handle("/v1/", a2av0.NewRESTHandler(reqHandler, transportOpts...))
	}

	// Fallback to v1.0 HTTP+JSON REST routes (/message:send, /message:stream,
	// /tasks, /tasks/{id}, /tasks/{id}:cancel, /tasks/{id}:subscribe,
	// /tasks/{id}/pushNotificationConfigs, /extendedAgentCard).
	mux.Handle("/", v1REST)
	return mux
}

// HTTPAddr returns the bound TCP address of the HTTP listener.
func (s *Server) HTTPAddr() string {
	if s.httpListener == nil {
		return ""
	}
	return s.httpListener.Addr().String()
}

// HTTPBaseURL returns the public HTTP base URL advertised in the AgentCard.
func (s *Server) HTTPBaseURL() string {
	return s.httpBaseURL
}

// GRPCAddr returns the bound TCP address of the gRPC listener, or "" if disabled.
func (s *Server) GRPCAddr() string {
	if s.grpcListener == nil {
		return ""
	}
	return s.grpcListener.Addr().String()
}

// PublicCard returns the public AgentCard served at /.well-known/agent-card.json.
func (s *Server) PublicCard() *a2a.AgentCard {
	return s.publicCard
}

// ExtendedCard returns the authenticated extended AgentCard, or nil if disabled.
func (s *Server) ExtendedCard() *a2a.AgentCard {
	return s.extendedCard
}

// Serve runs the HTTP (and optional gRPC) servers until ctx is canceled or a
// listener encounters a fatal error, then performs graceful shutdown.
func (s *Server) Serve(ctx context.Context) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	var wg sync.WaitGroup

	wg.Go(func() {
		if err := s.httpServer.Serve(s.httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
			cancel()
		}
	})

	if s.grpcServer != nil && s.grpcListener != nil {
		wg.Go(func() {
			if err := s.grpcServer.Serve(s.grpcListener); err != nil {
				errCh <- fmt.Errorf("gRPC server error: %w", err)
				cancel()
			}
		})
	}

	<-serveCtx.Done()
	s.shutdown()
	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Close stops the server and closes any bound listeners.
func (s *Server) Close() error {
	s.shutdown()
	return nil
}

func (s *Server) shutdown() {
	s.closeOnce.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if s.httpServer != nil {
			if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
				_ = s.httpServer.Close()
			}
		} else if s.httpListener != nil {
			_ = s.httpListener.Close()
		}

		if s.grpcServer != nil {
			done := make(chan struct{})
			go func() {
				s.grpcServer.GracefulStop()
				close(done)
			}()
			select {
			case <-done:
			case <-shutdownCtx.Done():
				s.grpcServer.Stop()
			}
		} else if s.grpcListener != nil {
			_ = s.grpcListener.Close()
		}
	})
}

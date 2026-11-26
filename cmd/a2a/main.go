package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/speed2exe/ag/a2aserver"
	"github.com/speed2exe/ag/app"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/setting"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := printer.New(ctx, os.Stderr)
	defer p.Flush()
	if err := mainErr(ctx, p); err != nil {
		p.Print(printer.ColorRed, err.Error(), "\n", printer.Reset)
		p.Flush()
		os.Exit(1)
	}
}

func mainErr(ctx context.Context, p *printer.Printer) error {
	settings, err := setting.InitA2AServer()
	if err != nil {
		return fmt.Errorf("failed to parse arguments: %w.\nUse -h/--help for usage", err)
	}

	rt, _, err := app.Init(ctx, p, settings)
	if err != nil {
		return err
	}
	defer rt.Close()

	srv, err := a2aserver.New(rt)
	if err != nil {
		return fmt.Errorf("failed to initialize A2A server: %w", err)
	}
	defer srv.Close()

	p.Log(fmt.Sprintf("A2A server %q listening on %s\n", srv.PublicCard().Name, srv.HTTPAddr()))
	p.Log(fmt.Sprintf("  Agent Card: %s%s\n", srv.HTTPBaseURL(), a2asrv.WellKnownAgentCardPath))
	p.Log(fmt.Sprintf("  JSON-RPC:   %s/jsonrpc\n", srv.HTTPBaseURL()))
	p.Log(fmt.Sprintf("  HTTP+JSON:  %s\n", srv.HTTPBaseURL()))
	if settings.A2aServerCompatV0 {
		p.Log(fmt.Sprintf("  v0.3 Card:  %s/.well-known/agent.json\n", srv.HTTPBaseURL()))
		p.Log(fmt.Sprintf("  v0.3 RPC:   %s/v0/jsonrpc\n", srv.HTTPBaseURL()))
		p.Log(fmt.Sprintf("  v0.3 REST:  %s/v1/...\n", srv.HTTPBaseURL()))
	}
	if grpcAddr := srv.GRPCAddr(); grpcAddr != "" {
		p.Log(fmt.Sprintf("  gRPC:       %s\n", grpcAddr))
	}
	p.Log(fmt.Sprintf("  Data dir:   %s\n", settings.A2aServerDataDir))
	if settings.A2aServerBearerToken != "" || settings.A2aServerApiKey != "" {
		p.Log("  Auth:       enabled\n")
	} else {
		p.Log("  Auth:       disabled\n")
	}
	p.Flush()

	if err := srv.Serve(ctx); err != nil {
		return fmt.Errorf("A2A server exited with error: %w", err)
	}
	p.Log("A2A server shut down gracefully\n")
	return nil
}

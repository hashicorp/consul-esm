package grpc

import (
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"net"
	"testing"
)

type GRPCService interface {
	Register(*grpc.Server)
}

func RunTestServerWithHealthStatus(t *testing.T, service GRPCService, healthStatus healthgrpc.HealthCheckResponse_ServingStatus) net.Addr {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := grpc.NewServer()
	healthcheck := health.NewServer()
	healthgrpc.RegisterHealthServer(s, healthcheck)
	service.Register(s)

	healthcheck.SetServingStatus("", healthStatus)

	go s.Serve(lis)
	t.Cleanup(s.Stop)

	return lis.Addr()
}

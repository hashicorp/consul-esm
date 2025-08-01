package testservice

import (
	"context"
	pb "github.com/hashicorp/consul-esm/testutils/grpc/testservice/hello"
	"google.golang.org/grpc"
	"log"
)

type Server struct {
	pb.UnimplementedHelloServiceServer
}

func (s *Server) Greet(_ context.Context, in *pb.HelloRequest) (*pb.HelloResponse, error) {
	log.Printf("Received greet request")
	return &pb.HelloResponse{Message: "Hi there!"}, nil
}

func (s *Server) Register(grpcServer *grpc.Server) {
	pb.RegisterHelloServiceServer(grpcServer, s)
}

func NewServer() *Server {
	return &Server{}
}

package server

import (
	"context"
	"fmt"

	"github.com/infisical/infisical-csi-provider/internal/config"
	"github.com/infisical/infisical-csi-provider/internal/provider"
	pb "sigs.k8s.io/secrets-store-csi-driver/provider/v1alpha1"
)

var _ pb.CSIDriverProviderServer = (*Server)(nil)

// Server implements the secrets-store-csi-driver provider gRPC service interface.
type Server struct {
	HostUrl string
}

func (s *Server) Version(context.Context, *pb.VersionRequest) (*pb.VersionResponse, error) {
	return &pb.VersionResponse{
		Version:     "v1alpha1",
		RuntimeName: "infisical-csi-provider",
		// TODO: set dynamically in the build process
		RuntimeVersion: "v0.0.1",
	}, nil
}

func (s *Server) Mount(ctx context.Context, req *pb.MountRequest) (*pb.MountResponse, error) {
	cfg, err := config.Parse(req.Attributes, req.TargetPath, req.Permission, s.HostUrl)
	if err != nil {
		return nil, err
	}

	provider := provider.NewProvider()
	resp, err := provider.HandleMountRequest(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("error making mount request: %w", err)
	}
	return resp, nil
}

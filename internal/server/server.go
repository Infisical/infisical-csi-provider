package server

import (
	"context"
	"fmt"

	"github.com/infisical/infisical-csi-provider/internal/config"
	"github.com/infisical/infisical-csi-provider/internal/provider"
	"github.com/infisical/infisical-csi-provider/internal/version"
	"github.com/infisical/infisical-csi-provider/internal/window"
	pb "sigs.k8s.io/secrets-store-csi-driver/provider/v1alpha1"
)

var _ pb.CSIDriverProviderServer = (*Server)(nil)

// Server implements the secrets-store-csi-driver provider gRPC service interface.
type Server struct {
	HostUrl string
	// Pods reads pod status to evaluate the read window, from a watch rather than a request per
	// mount: the rotation reconciler calls Mount for every pod on every poll.
	Pods window.PodSource
}

func (s *Server) Version(context.Context, *pb.VersionRequest) (*pb.VersionResponse, error) {
	return &pb.VersionResponse{
		Version:        "v1alpha1",
		RuntimeName:    "infisical-csi-provider",
		RuntimeVersion: version.BuildVersion,
	}, nil
}

func (s *Server) Mount(ctx context.Context, req *pb.MountRequest) (*pb.MountResponse, error) {
	cfg, err := config.Parse(ctx, req.Attributes, req.TargetPath, req.Permission, s.HostUrl)
	if err != nil {
		return nil, err
	}

	provider := provider.NewProvider(s.Pods)
	resp, err := provider.HandleMountRequest(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("error making mount request: %w", err)
	}
	return resp, nil
}

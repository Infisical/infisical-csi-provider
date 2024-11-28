package provider

import (
	"context"
	"fmt"
	"log"
	"strconv"

	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/infisical-csi-provider/internal/config"
	pb "sigs.k8s.io/secrets-store-csi-driver/provider/v1alpha1"
)

type Provider struct {
}

func NewProvider() *Provider {
	return &Provider{}
}

type secretItem struct {
	FileName string
	Value    string
	Version  string
}

func (p *Provider) HandleMountRequest(ctx context.Context, cfg config.Config) (*pb.MountResponse, error) {
	infisicalUrl := cfg.Parameters.InfisicalUrl
	if infisicalUrl == "" {
		infisicalUrl = cfg.HostUrl
	}

	infisicalClient := infisical.NewInfisicalClient(ctx, infisical.Config{
		SiteUrl:       infisicalUrl,
		CaCertificate: cfg.Parameters.CaCertificate,
	})

	_, err := infisicalClient.Auth().KubernetesRawServiceAccountTokenLogin(cfg.Parameters.IdentityId, cfg.Parameters.PodInfo.ServiceAccountToken)
	if err != nil {
		return nil, fmt.Errorf("unable to login with Kubernetes auth [err=%s]", err)
	}

	secretMap := make(map[string]*secretItem)
	for _, secret := range cfg.Parameters.Secrets {
		sec, err := infisicalClient.Secrets().Retrieve(infisical.RetrieveSecretOptions{
			SecretKey:      secret.SecretKey,
			ProjectID:      cfg.Parameters.ProjectId,
			Environment:    cfg.Parameters.EnvSlug,
			SecretPath:     secret.SecretPath,
			IncludeImports: true,
		})

		if err != nil {
			return nil, err
		}

		secretMap[sec.ID] = &secretItem{
			FileName: secret.FileName,
			Value:    sec.SecretValue,
			Version:  fmt.Sprintf("%s-%s-%s-%s", sec.ID, sec.SecretPath, sec.SecretKey, strconv.Itoa(sec.Version)),
		}
	}

	var files []*pb.File
	var ov []*pb.ObjectVersion

	for _, value := range secretMap {
		files = append(files, &pb.File{Path: value.FileName, Mode: int32(cfg.FilePermission), Contents: []byte(value.Value)})
		ov = append(ov, &pb.ObjectVersion{Id: value.FileName, Version: value.Version})
		log.Printf("secret added to mount response, directory: %v, file: %v", cfg.TargetPath, value.FileName)
	}

	return &pb.MountResponse{
		ObjectVersion: ov,
		Files:         files,
	}, nil
}

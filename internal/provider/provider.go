package provider

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"

	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/infisical-csi-provider/internal/config"
	"github.com/infisical/infisical-csi-provider/internal/window"
	pb "sigs.k8s.io/secrets-store-csi-driver/provider/v1alpha1"
)

// rotationReminder logs the driver-rotation requirement once per process, the first time a read
// window is evaluated. The window only ever closes when the driver re-invokes the provider, which it
// does only with secret rotation enabled; without it Mount is called once at publish time, when the
// window is always open, so the secret is served and never blanked. A MountRequest carries no signal
// of the driver's rotation setting, so this is a reminder rather than a check: it keeps the feature
// from silently doing nothing while an operator believes exposure is bounded.
var rotationReminder sync.Once

type Provider struct {
	pods window.PodSource
}

func NewProvider(pods window.PodSource) *Provider {
	return &Provider{pods: pods}
}

type secretItem struct {
	FileName string
	Value    string
	Version  string
}

func (p *Provider) HandleMountRequest(ctx context.Context, cfg config.Config) (*pb.MountResponse, error) {
	// Outside its read window a pod is served empty files, and Infisical is not called at all: the
	// secret never leaves the platform, rather than being fetched and then withheld.
	if cfg.Parameters.WindowDuration > 0 {
		rotationReminder.Do(func() {
			log.Print("windowDuration is set: the read window only takes effect when the driver runs with " +
				"--enable-secret-rotation=true and a short --rotation-poll-interval. Without rotation the provider " +
				"is called once at mount time, the window never closes, and secrets stay readable for the pod's whole life.")
		})
		open, err := window.IsOpen(ctx, p.pods, window.Pod{
			Namespace: cfg.Parameters.PodInfo.Namespace,
			Name:      cfg.Parameters.PodInfo.Name,
			UID:       string(cfg.Parameters.PodInfo.UID),
			Volume:    cfg.VolumeName,
		}, cfg.Parameters.WindowDuration)
		if err != nil {
			return nil, err
		}
		if !open {
			log.Printf("read window closed for pod %s/%s, serving empty files",
				cfg.Parameters.PodInfo.Namespace, cfg.Parameters.PodInfo.Name)
			return emptyResponse(cfg), nil
		}
	}

	infisicalUrl := cfg.Parameters.InfisicalUrl
	if infisicalUrl == "" {
		infisicalUrl = cfg.HostUrl
	}

	infisicalClient := infisical.NewInfisicalClient(ctx, infisical.Config{
		SiteUrl:       infisicalUrl,
		CaCertificate: cfg.Parameters.CaCertificate,
	})

	// AWS auth signs an sts:GetCallerIdentity with the provider's own ambient credentials, so it works
	// on clusters whose API server Infisical cannot reach. Kubernetes auth needs Infisical to call
	// TokenReview, which a private API endpoint rules out.
	//
	// Every method is named rather than folded into a default, so adding one to the values config
	// accepts without handling it here fails as an unsupported method instead of silently attempting a
	// Kubernetes login with no token.
	var err error
	switch cfg.Parameters.AuthMethod {
	case config.AuthAws:
		_, err = infisicalClient.Auth().AwsIamAuthLogin(cfg.Parameters.IdentityId)
	case config.AuthKubernetes:
		_, err = infisicalClient.Auth().KubernetesRawServiceAccountTokenLogin(
			cfg.Parameters.IdentityId, cfg.Parameters.PodInfo.ServiceAccountToken)
	default:
		return nil, fmt.Errorf("unsupported auth method %q", cfg.Parameters.AuthMethod)
	}
	if err != nil {
		return nil, fmt.Errorf("unable to login with %s auth [err=%s]", cfg.Parameters.AuthMethod, err)
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

// closedVersion is reported for every file while the window is shut. It is deliberately constant: the
// driver writes back whatever payload we return regardless of the version, and reporting a stable one
// keeps it from raising a rotation event and rewriting the pod status on every poll.
const closedVersion = "read-window-closed"

// emptyResponse serves every configured file with no content, so the driver overwrites what it
// previously wrote.
func emptyResponse(cfg config.Config) *pb.MountResponse {
	files := make([]*pb.File, 0, len(cfg.Parameters.Secrets))
	ov := make([]*pb.ObjectVersion, 0, len(cfg.Parameters.Secrets))
	for _, secret := range cfg.Parameters.Secrets {
		files = append(files, &pb.File{Path: secret.FileName, Mode: int32(cfg.FilePermission), Contents: []byte{}})
		ov = append(ov, &pb.ObjectVersion{Id: secret.FileName, Version: closedVersion})
	}
	return &pb.MountResponse{ObjectVersion: ov, Files: files}
}

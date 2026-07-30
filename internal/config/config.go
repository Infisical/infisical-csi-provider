package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v2"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/secrets-store-csi-driver/pkg/util/fileutil"
)

// Auth methods the provider supports. Shared with the login switch so the accepted values and the
// handled ones cannot drift apart.
const (
	AuthKubernetes = "kubernetes"
	AuthAws        = "aws"
)

type Config struct {
	Parameters     Parameters
	TargetPath     string
	FilePermission os.FileMode
	HostUrl        string
	// VolumeName is the CSI volume this mount publishes, recovered from TargetPath. The MountRequest
	// carries no volume field, and the driver itself parses the path the same way to correlate a target
	// path with a pod volume.
	VolumeName string
}

type PodInfo struct {
	Name                string
	UID                 types.UID
	Namespace           string
	ServiceAccountName  string
	ServiceAccountToken string
}

type Parameters struct {
	Audience           string
	UseDefaultAudience bool
	AuthMethod         string
	InfisicalUrl       string
	Secrets            []Secret
	PodInfo            PodInfo
	CaCertificate      string
	IdentityId         string
	ProjectId          string
	EnvSlug            string
	// WindowDuration bounds how long after its containers start a pod is served its secrets. Zero, the
	// default, serves them for the pod's whole life, which is the historical behaviour.
	WindowDuration time.Duration
}

type Secret struct {
	FileName   string `yaml:"fileName"`
	SecretPath string `yaml:"secretPath"`
	SecretKey  string `yaml:"secretKey"`
}

func createJWTTokenWithDefaultAudience(ctx context.Context, parameters Parameters) (string, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return "", fmt.Errorf("failed to create in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	podInfo := parameters.PodInfo
	ttl := int64((15 * time.Minute).Seconds())

	resp, err := clientset.CoreV1().ServiceAccounts(podInfo.Namespace).CreateToken(ctx, podInfo.ServiceAccountName, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: &ttl,
			BoundObjectRef: &authenticationv1.BoundObjectReference{
				Kind:       "Pod",
				APIVersion: "v1",
				Name:       podInfo.Name,
				UID:        podInfo.UID,
			},
		},
	}, metav1.CreateOptions{})

	if err != nil {
		return "", fmt.Errorf("failed to create a service account token for requesting pod %v: %w", podInfo, err)
	}

	return resp.Status.Token, nil
}

func parseParameters(ctx context.Context, parametersStr string) (Parameters, error) {
	var params map[string]string
	err := json.Unmarshal([]byte(parametersStr), &params)
	if err != nil {
		return Parameters{}, err
	}

	var parameters Parameters

	parameters.AuthMethod = params["authMethod"]

	if parameters.AuthMethod == "" {
		parameters.AuthMethod = AuthKubernetes
	}
	if parameters.AuthMethod != AuthKubernetes && parameters.AuthMethod != AuthAws {
		return Parameters{}, fmt.Errorf("invalid value for auth method - valid options are %s and %s",
			AuthKubernetes, AuthAws)
	}

	parameters.Audience = params["audience"]
	if parameters.Audience == "" {
		parameters.Audience = "infisical"
	}

	parameters.UseDefaultAudience = params["useDefaultAudience"] == "true"

	parameters.InfisicalUrl = params["infisicalUrl"]
	parameters.CaCertificate = params["caCertificate"]
	parameters.IdentityId = params["identityId"]
	parameters.ProjectId = params["projectId"]
	parameters.EnvSlug = params["envSlug"]

	if raw := params["windowDuration"]; raw != "" {
		parameters.WindowDuration, err = time.ParseDuration(raw)
		if err != nil {
			return Parameters{}, fmt.Errorf("invalid windowDuration %q: %w", raw, err)
		}
		if parameters.WindowDuration <= 0 {
			return Parameters{}, fmt.Errorf("windowDuration must be positive, got %q", raw)
		}
	}

	parameters.PodInfo.Name = params["csi.storage.k8s.io/pod.name"]
	parameters.PodInfo.UID = types.UID(params["csi.storage.k8s.io/pod.uid"])
	parameters.PodInfo.Namespace = params["csi.storage.k8s.io/pod.namespace"]
	parameters.PodInfo.ServiceAccountName = params["csi.storage.k8s.io/serviceAccount.name"]

	// Only kubernetes auth logs in with the pod's token; AWS auth signs with the provider's own
	// ambient credentials.
	if parameters.AuthMethod == AuthKubernetes {
		parameters.PodInfo.ServiceAccountToken, err = serviceAccountToken(
			ctx, parameters, params["csi.storage.k8s.io/serviceAccount.tokens"])
		if err != nil {
			return Parameters{}, err
		}
	}

	secretsYaml := params["secrets"]
	if secretsYaml != "" {
		err = yaml.Unmarshal([]byte(secretsYaml), &parameters.Secrets)
		if err != nil {
			return Parameters{}, err
		}
	}

	return parameters, nil
}

func Parse(ctx context.Context, parametersStr string, targetPath string, permissionStr string, hostUrl string) (Config, error) {
	config := Config{
		TargetPath: targetPath,
		HostUrl:    hostUrl,
	}

	var err error
	config.Parameters, err = parseParameters(ctx, parametersStr)
	if err != nil {
		return Config{}, err
	}

	if err := json.Unmarshal([]byte(permissionStr), &config.FilePermission); err != nil {
		return Config{}, err
	}

	config.VolumeName = fileutil.GetVolumeNameFromTargetPath(targetPath)

	if config.Parameters.InfisicalUrl != "" {
		config.HostUrl = config.Parameters.InfisicalUrl
	}

	err = config.Validate()
	if err != nil {
		return Config{}, err
	}

	return config, nil
}

func (cfg *Config) Validate() error {
	if cfg.HostUrl == "" {
		return fmt.Errorf("infisical url must be defined")
	}

	if cfg.Parameters.IdentityId == "" {
		return fmt.Errorf("identity id must be defined")
	}

	if cfg.Parameters.ProjectId == "" {
		return fmt.Errorf("project id must be defined")
	}

	if cfg.Parameters.EnvSlug == "" {
		return fmt.Errorf("env slug must be defined")
	}

	if len(cfg.Parameters.Secrets) == 0 {
		return fmt.Errorf("must have at least one secret")
	}

	return nil
}

// serviceAccountToken returns the pod token that kubernetes auth logs in with, either minted with the
// default audience or taken from the tokens the driver passed for ours.
func serviceAccountToken(ctx context.Context, parameters Parameters, tokensJSON string) (string, error) {
	if parameters.UseDefaultAudience {
		return createJWTTokenWithDefaultAudience(ctx, parameters)
	}

	// The csi.storage.k8s.io/serviceAccount.tokens field is a JSON object marshalled into a string. The
	// object keys are audience name (string) and the values are embedded objects with a "token" property.
	var tokens map[string]struct {
		Token string `json:"token"`
	}
	if tokensJSON != "" {
		if err := json.Unmarshal([]byte(tokensJSON), &tokens); err != nil {
			return "", fmt.Errorf("failed to unmarshal service account tokens: %w", err)
		}
	}

	token := tokens[parameters.Audience].Token
	if token == "" {
		return "", fmt.Errorf("no service account token received")
	}
	return token, nil
}

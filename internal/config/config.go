package config

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/types"
)

type Config struct {
	Parameters     Parameters
	TargetPath     string
	FilePermission os.FileMode
	HostUrl        string
}

type PodInfo struct {
	Name                string
	UID                 types.UID
	Namespace           string
	ServiceAccountName  string
	ServiceAccountToken string
}

type Parameters struct {
	Audience      string
	AuthMethod    string
	InfisicalUrl  string
	Secrets       []Secret
	PodInfo       PodInfo
	CaCertificate string
	IdentityId    string
	ProjectId     string
	EnvSlug       string
}

type Secret struct {
	FileName   string `yaml:"fileName"`
	SecretPath string `yaml:"secretPath"`
	SecretKey  string `yaml:"secretKey"`
}

func parseParameters(parametersStr string) (Parameters, error) {
	var params map[string]string
	err := json.Unmarshal([]byte(parametersStr), &params)
	if err != nil {
		return Parameters{}, err
	}

	var parameters Parameters

	parameters.AuthMethod = params["authMethod"]

	// for now, only kubernetes auth is supported
	if parameters.AuthMethod != "kubernetes" {
		return Parameters{}, fmt.Errorf("invalid value for auth method - valid option is kubernetes")
	}

	parameters.Audience = params["audience"]
	if parameters.Audience == "" {
		parameters.Audience = "infisical"
	}

	parameters.InfisicalUrl = params["infisicalUrl"]
	parameters.CaCertificate = params["caCertificate"]
	parameters.IdentityId = params["identityId"]
	parameters.ProjectId = params["projectId"]
	parameters.EnvSlug = params["envSlug"]

	parameters.PodInfo.Name = params["csi.storage.k8s.io/pod.name"]
	parameters.PodInfo.UID = types.UID(params["csi.storage.k8s.io/pod.uid"])
	parameters.PodInfo.Namespace = params["csi.storage.k8s.io/pod.namespace"]
	parameters.PodInfo.ServiceAccountName = params["csi.storage.k8s.io/serviceAccount.name"]

	tokensJSON := params["csi.storage.k8s.io/serviceAccount.tokens"]
	if tokensJSON != "" {
		// The csi.storage.k8s.io/serviceAccount.tokens field is a JSON object
		// marshalled into a string. The object keys are audience name (string)
		// and the values are embedded objects with "token" property
		var tokens map[string]struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(tokensJSON), &tokens); err != nil {
			return Parameters{}, fmt.Errorf("failed to unmarshal service account tokens: %w", err)
		}

		if token, ok := tokens[parameters.Audience]; ok {
			parameters.PodInfo.ServiceAccountToken = token.Token
		}
	}

	if parameters.PodInfo.ServiceAccountToken == "" {
		return Parameters{}, fmt.Errorf("no service account token received")
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

func Parse(parametersStr string, targetPath string, permissionStr string, hostUrl string) (Config, error) {
	config := Config{
		TargetPath: targetPath,
		HostUrl:    hostUrl,
	}

	var err error
	config.Parameters, err = parseParameters(parametersStr)
	if err != nil {
		return Config{}, err
	}

	if err := json.Unmarshal([]byte(permissionStr), &config.FilePermission); err != nil {
		return Config{}, err
	}

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

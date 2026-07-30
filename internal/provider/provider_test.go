package provider

import (
	"fmt"
	"strings"
	"testing"

	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"
	"github.com/infisical/infisical-csi-provider/internal/config"
)

// fakeSecrets serves a fixed folder layout and counts what was asked of it.
type fakeSecrets struct {
	infisical.SecretsInterface

	folders map[string][]models.Secret
	calls   []infisical.ListSecretsOptions
	err     error
}

func (f *fakeSecrets) List(options infisical.ListSecretsOptions) ([]models.Secret, error) {
	f.calls = append(f.calls, options)
	if f.err != nil {
		return nil, f.err
	}
	return f.folders[options.SecretPath], nil
}

func secret(key string) models.Secret {
	return models.Secret{ID: "id-" + key, SecretKey: key, SecretValue: "value-" + key, Version: 1}
}

func configWith(secrets ...config.Secret) config.Config {
	return config.Config{
		FilePermission: 0o644,
		Parameters: config.Parameters{
			ProjectId: "project",
			EnvSlug:   "prod",
			Secrets:   secrets,
		},
	}
}

// The reason this file exists: a mount asking for many keys from one folder must cost one request,
// not one per key, because the driver repeats it on every rotation poll while the window is open.
func TestOneListPerPathNotPerKey(t *testing.T) {
	requested := make([]config.Secret, 0, 20)
	folder := make([]models.Secret, 0, 20)
	for i := range 20 {
		key := fmt.Sprintf("KEY_%d", i)
		requested = append(requested, config.Secret{FileName: key, SecretKey: key, SecretPath: "/"})
		folder = append(folder, secret(key))
	}

	client := &fakeSecrets{folders: map[string][]models.Secret{"/": folder}}

	resp, err := secretsResponse(client, configWith(requested...))
	if err != nil {
		t.Fatalf("secretsResponse: %v", err)
	}

	if len(client.calls) != 1 {
		t.Errorf("expected 1 List call for 20 keys in one folder, got %d", len(client.calls))
	}
	if len(resp.Files) != 20 || len(resp.ObjectVersion) != 20 {
		t.Fatalf("expected 20 files and 20 versions, got %d and %d", len(resp.Files), len(resp.ObjectVersion))
	}
	if got := string(resp.Files[3].Contents); got != "value-KEY_3" {
		t.Errorf("files[3] = %q, want %q", got, "value-KEY_3")
	}
}

func TestOneListPerDistinctPath(t *testing.T) {
	client := &fakeSecrets{folders: map[string][]models.Secret{
		"/":        {secret("A"), secret("B")},
		"/nested/": {secret("C")},
	}}

	_, err := secretsResponse(client, configWith(
		config.Secret{FileName: "A", SecretKey: "A", SecretPath: "/"},
		config.Secret{FileName: "B", SecretKey: "B", SecretPath: "/"},
		config.Secret{FileName: "C", SecretKey: "C", SecretPath: "/nested/"},
	))
	if err != nil {
		t.Fatalf("secretsResponse: %v", err)
	}

	if len(client.calls) != 2 {
		t.Errorf("expected 2 List calls for 2 distinct paths, got %d", len(client.calls))
	}
}

// An empty folder must not be re-listed once per key that wanted something from it.
func TestEmptyFolderIsListedOnce(t *testing.T) {
	client := &fakeSecrets{folders: map[string][]models.Secret{}}

	_, err := secretsResponse(client, configWith(
		config.Secret{FileName: "A", SecretKey: "A", SecretPath: "/"},
		config.Secret{FileName: "B", SecretKey: "B", SecretPath: "/"},
	))
	if err == nil {
		t.Fatal("expected an error for a key that does not exist")
	}

	if len(client.calls) != 1 {
		t.Errorf("expected 1 List call, got %d", len(client.calls))
	}
}

// Retrieve failed the mount on a missing key, and dropping to a listing must not turn that into a
// file that is quietly absent.
func TestMissingKeyFailsTheMount(t *testing.T) {
	client := &fakeSecrets{folders: map[string][]models.Secret{"/": {secret("PRESENT")}}}

	_, err := secretsResponse(client, configWith(
		config.Secret{FileName: "PRESENT", SecretKey: "PRESENT", SecretPath: "/"},
		config.Secret{FileName: "ABSENT", SecretKey: "ABSENT", SecretPath: "/"},
	))
	if err == nil {
		t.Fatal("expected an error naming the missing key")
	}
	if got := err.Error(); !strings.Contains(got, "ABSENT") {
		t.Errorf("error should name the missing key, got %q", got)
	}
}

// Two files fed by the same Infisical secret used to collide: the response was keyed by secret ID, so
// the second entry replaced the first and only one file was written.
func TestSameSecretCanFeedTwoFiles(t *testing.T) {
	client := &fakeSecrets{folders: map[string][]models.Secret{"/": {secret("TOKEN")}}}

	resp, err := secretsResponse(client, configWith(
		config.Secret{FileName: "TOKEN", SecretKey: "TOKEN", SecretPath: "/"},
		config.Secret{FileName: "TOKEN_COPY", SecretKey: "TOKEN", SecretPath: "/"},
	))
	if err != nil {
		t.Fatalf("secretsResponse: %v", err)
	}

	if len(resp.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(resp.Files))
	}
	if resp.Files[0].Path != "TOKEN" || resp.Files[1].Path != "TOKEN_COPY" {
		t.Errorf("unexpected file names: %q and %q", resp.Files[0].Path, resp.Files[1].Path)
	}
}

func TestListErrorFailsTheMount(t *testing.T) {
	client := &fakeSecrets{err: fmt.Errorf("infisical is down")}

	if _, err := secretsResponse(client, configWith(
		config.Secret{FileName: "A", SecretKey: "A", SecretPath: "/"},
	)); err == nil {
		t.Fatal("expected the List error to fail the mount")
	}
}

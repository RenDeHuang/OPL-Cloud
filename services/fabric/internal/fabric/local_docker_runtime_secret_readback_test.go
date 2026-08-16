package fabric

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type runtimeSecretArchiveEntry struct {
	name     string
	typeflag byte
	body     []byte
	mode     int64
}

func runtimeSecretArchive(t *testing.T, entries []runtimeSecretArchiveEntry) []byte {
	t.Helper()
	var body bytes.Buffer
	writer := tar.NewWriter(&body)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Mode: entry.mode, Size: int64(len(entry.body))}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) > 0 {
			if _, err := writer.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func validRuntimeSecretArchive(t *testing.T, key []byte, metadata localDockerGatewayMetadata) []byte {
	t.Helper()
	meta, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeSecretArchive(t, []runtimeSecretArchiveEntry{
		{name: "./", typeflag: tar.TypeDir, mode: 0700},
		{name: "./" + localDockerGatewayKeyFile, typeflag: tar.TypeReg, body: key, mode: 0444},
		{name: "./" + localDockerGatewayMetaFile, typeflag: tar.TypeReg, body: meta, mode: 0400},
	})
}

func runtimeSecretMountContainer(t *testing.T, mutate func(map[string]any)) dockerContainerInspect {
	t.Helper()
	value := map[string]any{
		"Id":   "container-runtime",
		"Name": "/opl-runtime-test",
		"HostConfig": map[string]any{"Mounts": []any{
			map[string]any{"Type": "volume", "Source": "storage-test", "Target": "/data"},
			map[string]any{"Type": "bind", "Source": "/private/host/secret", "Target": "/run/secrets", "ReadOnly": true, "BindOptions": map[string]any{"Propagation": "rprivate"}},
		}},
		"Mounts": []any{
			map[string]any{"Type": "volume", "Name": "storage-test", "Source": "/var/lib/docker/volumes/storage-test/_data", "Destination": "/data", "RW": true},
			map[string]any{"Type": "bind", "Source": "/host_mnt/private/host/secret", "Destination": "/run/secrets", "RW": false, "Propagation": "rprivate"},
		},
	}
	if mutate != nil {
		mutate(value)
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var container dockerContainerInspect
	if err := json.Unmarshal(body, &container); err != nil {
		t.Fatal(err)
	}
	return container
}

func TestLocalDockerRuntimeSecretMountViewsFailClosed(t *testing.T) {
	appendHostMount := func(value map[string]any, mount map[string]any) {
		host := value["HostConfig"].(map[string]any)
		host["Mounts"] = append(host["Mounts"].([]any), mount)
	}
	appendRuntimeMount := func(value map[string]any, mount map[string]any) {
		value["Mounts"] = append(value["Mounts"].([]any), mount)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
		valid  bool
	}{
		{name: "virtualized source differs", valid: true},
		{name: "duplicate HostConfig target", mutate: func(value map[string]any) {
			appendHostMount(value, map[string]any{"Type": "bind", "Source": "/other", "Target": "/run/secrets", "ReadOnly": true, "BindOptions": map[string]any{"Propagation": "rprivate"}})
			appendRuntimeMount(value, map[string]any{"Type": "bind", "Source": "/other-runtime", "Destination": "/other", "RW": false, "Propagation": "rprivate"})
		}},
		{name: "duplicate runtime target", mutate: func(value map[string]any) {
			appendHostMount(value, map[string]any{"Type": "bind", "Source": "/other-host", "Target": "/other", "ReadOnly": true, "BindOptions": map[string]any{"Propagation": "rprivate"}})
			appendRuntimeMount(value, map[string]any{"Type": "bind", "Source": "/other", "Destination": "/run/secrets", "RW": false, "Propagation": "rprivate"})
		}},
		{name: "HostConfig source mounted elsewhere", mutate: func(value map[string]any) {
			appendHostMount(value, map[string]any{"Type": "bind", "Source": "/private/host/secret", "Target": "/leak", "ReadOnly": true, "BindOptions": map[string]any{"Propagation": "rprivate"}})
			appendRuntimeMount(value, map[string]any{"Type": "bind", "Source": "/other-runtime", "Destination": "/leak", "RW": false, "Propagation": "rprivate"})
		}},
		{name: "runtime source mounted elsewhere", mutate: func(value map[string]any) {
			appendHostMount(value, map[string]any{"Type": "bind", "Source": "/other-host", "Target": "/leak", "ReadOnly": true, "BindOptions": map[string]any{"Propagation": "rprivate"}})
			appendRuntimeMount(value, map[string]any{"Type": "bind", "Source": "/host_mnt/private/host/secret", "Destination": "/leak", "RW": false, "Propagation": "rprivate"})
		}},
		{name: "HostConfig writable", mutate: func(value map[string]any) {
			value["HostConfig"].(map[string]any)["Mounts"].([]any)[1].(map[string]any)["ReadOnly"] = false
		}},
		{name: "runtime writable", mutate: func(value map[string]any) {
			value["Mounts"].([]any)[1].(map[string]any)["RW"] = true
		}},
		{name: "HostConfig propagation drift", mutate: func(value map[string]any) {
			value["HostConfig"].(map[string]any)["Mounts"].([]any)[1].(map[string]any)["BindOptions"] = map[string]any{"Propagation": "rshared"}
		}},
		{name: "runtime propagation drift", mutate: func(value map[string]any) {
			value["Mounts"].([]any)[1].(map[string]any)["Propagation"] = "rshared"
		}},
		{name: "HostConfig type drift", mutate: func(value map[string]any) {
			value["HostConfig"].(map[string]any)["Mounts"].([]any)[1].(map[string]any)["Type"] = "volume"
		}},
		{name: "runtime type drift", mutate: func(value map[string]any) {
			value["Mounts"].([]any)[1].(map[string]any)["Type"] = "volume"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validRuntimeSecretMountViews(runtimeSecretMountContainer(t, test.mutate)); got != test.valid {
				t.Fatalf("valid=%v want=%v", got, test.valid)
			}
		})
	}
}

func TestLocalDockerRuntimeSecretArchiveFailsClosed(t *testing.T) {
	key := []byte("runtime-secret-key")
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	metadata := localDockerGatewayMetadata{
		AccountID: "acct-runtime", WorkspaceID: "ws-runtime", WorkspaceAPIKeyID: 7, SecretRef: gatewaySecretName("ws-runtime"),
		Fingerprint: "sha256:" + digest, Version: digest[:16],
	}
	valid := validRuntimeSecretArchive(t, key, metadata)
	metadataBody, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{name: "valid", body: valid, want: true},
		{name: "invalid", body: []byte("not a tar archive")},
		{name: "truncated", body: valid[:1800]},
		{name: "symlink key", body: runtimeSecretArchive(t, []runtimeSecretArchiveEntry{
			{name: "./", typeflag: tar.TypeDir, mode: 0700},
			{name: "./" + localDockerGatewayKeyFile, typeflag: tar.TypeSymlink, mode: 0444},
			{name: "./" + localDockerGatewayMetaFile, typeflag: tar.TypeReg, body: metadataBody, mode: 0400},
		})},
		{name: "duplicate key", body: runtimeSecretArchive(t, []runtimeSecretArchiveEntry{
			{name: "./", typeflag: tar.TypeDir, mode: 0700},
			{name: "./" + localDockerGatewayKeyFile, typeflag: tar.TypeReg, body: key, mode: 0444},
			{name: "./" + localDockerGatewayKeyFile, typeflag: tar.TypeReg, body: key, mode: 0444},
			{name: "./" + localDockerGatewayMetaFile, typeflag: tar.TypeReg, body: metadataBody, mode: 0400},
		})},
		{name: "extra entry", body: runtimeSecretArchive(t, []runtimeSecretArchiveEntry{
			{name: "./", typeflag: tar.TypeDir, mode: 0700},
			{name: "./" + localDockerGatewayKeyFile, typeflag: tar.TypeReg, body: key, mode: 0444},
			{name: "./" + localDockerGatewayMetaFile, typeflag: tar.TypeReg, body: metadataBody, mode: 0400},
			{name: "./extra", typeflag: tar.TypeReg, body: []byte("extra"), mode: 0400},
		})},
		{name: "missing metadata", body: runtimeSecretArchive(t, []runtimeSecretArchiveEntry{
			{name: "./", typeflag: tar.TypeDir, mode: 0700},
			{name: "./" + localDockerGatewayKeyFile, typeflag: tar.TypeReg, body: key, mode: 0444},
		})},
		{name: "missing key", body: runtimeSecretArchive(t, []runtimeSecretArchiveEntry{
			{name: "./", typeflag: tar.TypeDir, mode: 0700},
			{name: "./" + localDockerGatewayMetaFile, typeflag: tar.TypeReg, body: metadataBody, mode: 0400},
		})},
		{name: "unknown metadata field", body: runtimeSecretArchive(t, []runtimeSecretArchiveEntry{
			{name: "./", typeflag: tar.TypeDir, mode: 0700},
			{name: "./" + localDockerGatewayKeyFile, typeflag: tar.TypeReg, body: key, mode: 0444},
			{name: "./" + localDockerGatewayMetaFile, typeflag: tar.TypeReg, body: append(metadataBody[:len(metadataBody)-1], []byte(`,"unknown":true}`)...), mode: 0400},
		})},
		{name: "key fingerprint drift", body: validRuntimeSecretArchive(t, []byte("foreign-key"), metadata)},
	}
	for _, field := range []string{"accountId", "workspaceId", "workspaceApiKeyId", "secretRef", "fingerprint", "version"} {
		drifted := metadata
		switch field {
		case "accountId":
			drifted.AccountID = "acct-foreign"
		case "workspaceId":
			drifted.WorkspaceID = "ws-foreign"
		case "workspaceApiKeyId":
			drifted.WorkspaceAPIKeyID++
		case "secretRef":
			drifted.SecretRef = gatewaySecretName("ws-foreign")
		case "fingerprint":
			drifted.Fingerprint = "sha256:" + strings.Repeat("f", 64)
		case "version":
			drifted.Version = strings.Repeat("f", 16)
		}
		tests = append(tests, struct {
			name string
			body []byte
			want bool
		}{name: field + " drift", body: validRuntimeSecretArchive(t, key, drifted)})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRuntimeGatewaySecretArchive(test.body, metadata)
			if (err == nil) != test.want {
				t.Fatalf("err=%v wantValid=%v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), string(key)) {
				t.Fatalf("raw key leaked in error: %v", err)
			}
		})
	}
}

type failingRuntimeSecretArchiveRunner struct{}

func (failingRuntimeSecretArchiveRunner) Run(context.Context, []byte, ...string) ([]byte, error) {
	return nil, errors.New("archive unavailable")
}

func TestLocalDockerRuntimeSecretArchiveReadbackFailsClosedOnDockerError(t *testing.T) {
	provider := &LocalDockerProvider{runner: failingRuntimeSecretArchiveRunner{}}
	key := []byte("runtime-secret-key")
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	metadata := localDockerGatewayMetadata{
		AccountID: "acct-runtime", WorkspaceID: "ws-runtime", WorkspaceAPIKeyID: 7, SecretRef: gatewaySecretName("ws-runtime"),
		Fingerprint: "sha256:" + digest, Version: digest[:16],
	}
	container := runtimeSecretMountContainer(t, nil)
	container.Config.Labels = map[string]string{
		"opl.account.id": metadata.AccountID, "opl.workspace.id": metadata.WorkspaceID, "opl.secret.ref": metadata.SecretRef,
		"opl.secret.version": metadata.Version, "opl.secret.fingerprint": metadata.Fingerprint,
	}
	if err := provider.verifyRuntimeGatewaySecret(context.Background(), container, metadata); err == nil {
		t.Fatal("Docker archive error accepted")
	}
}

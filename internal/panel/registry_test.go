package panel

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRenderRegistryUsesContainerReachableGatewayWithoutLeakingElsewhere(t *testing.T) {
	token := []byte("gateway-secret")
	data, err := RenderRegistry(RegistryInput{InstanceID: "default", InstanceName: "MLink Local", GatewayEndpoint: "http://host.docker.internal:8420", GatewayToken: token})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Instances []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			GatewayEndpoint string `json:"gateway_endpoint"`
			APIKey          string `json:"api_key"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Instances) != 1 || decoded.Instances[0].GatewayEndpoint != "http://host.docker.internal:8420" || decoded.Instances[0].APIKey != string(token) {
		t.Fatalf("registry = %#v", decoded)
	}
	for _, forbidden := range [][]byte{[]byte("8096"), []byte("proxy_endpoint")} {
		if bytes.Contains(data, forbidden) {
			t.Fatalf("registry contains forbidden value %q", forbidden)
		}
	}
}

func TestRenderRegistryRejectsUnsafeInput(t *testing.T) {
	for _, input := range []RegistryInput{
		{InstanceID: "", InstanceName: "MLink", GatewayEndpoint: "http://host.docker.internal:8420", GatewayToken: []byte("token")},
		{InstanceID: "default", InstanceName: "MLink", GatewayEndpoint: "http://127.0.0.1:8420", GatewayToken: []byte("token")},
		{InstanceID: "default", InstanceName: "MLink", GatewayEndpoint: "http://host.docker.internal:8420", GatewayToken: nil},
	} {
		if _, err := RenderRegistry(input); err == nil {
			t.Fatalf("RenderRegistry(%#v) error = nil", input)
		}
	}
}

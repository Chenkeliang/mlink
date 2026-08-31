package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var registryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type RegistryInput struct {
	InstanceID      string
	InstanceName    string
	GatewayEndpoint string
	GatewayToken    []byte
}

func RenderRegistry(input RegistryInput) ([]byte, error) {
	endpoint, err := url.Parse(input.GatewayEndpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Host != "host.docker.internal:8420" || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
		return nil, errors.New("Panel Gateway endpoint must be http://host.docker.internal:8420")
	}
	if !registryIDPattern.MatchString(input.InstanceID) || strings.TrimSpace(input.InstanceName) == "" || len(input.InstanceName) > 128 || len(input.GatewayToken) == 0 || bytes.ContainsAny(input.GatewayToken, "\r\n") {
		return nil, errors.New("complete safe Panel registry input is required")
	}
	document := struct {
		Instances []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			GatewayEndpoint string `json:"gateway_endpoint"`
			APIKey          string `json:"api_key"`
		} `json:"instances"`
	}{}
	document.Instances = append(document.Instances, struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		GatewayEndpoint string `json:"gateway_endpoint"`
		APIKey          string `json:"api_key"`
	}{ID: input.InstanceID, Name: input.InstanceName, GatewayEndpoint: endpoint.String(), APIKey: string(input.GatewayToken)})
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, errors.New("encode Panel instance registry")
	}
	return append(data, '\n'), nil
}

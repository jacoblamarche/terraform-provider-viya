package viya_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viya"
	"github.com/jacoblamarche/terraform-provider-viya/internal/viyamock"
)

const (
	loggingDefinition = "logging.level"
	loggingMediaType  = "application/vnd.sas.configuration.config.logging.level+json;version=1"
)

func TestConfiguration_definitionName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		loggingMediaType: loggingDefinition,
		"application/vnd.sas.configuration.config.sas.compute.server+json": "sas.compute.server",
		"application/json":                    "",
		"application/vnd.sas.collection+json": "",
		"":                                    "",
	}
	for input, want := range tests {
		configuration := viya.Configuration{Metadata: viya.ConfigurationMetadata{MediaType: input}}
		if got := configuration.DefinitionName(); got != want {
			t.Errorf("DefinitionName for %q = %q, want %q", input, got, want)
		}
	}
}

func TestConfiguration_jsonRoundTrip(t *testing.T) {
	t.Parallel()

	const input = `{
		"id": "abc",
		"metadata": {
			"isDefault": true,
			"mediaType": "application/vnd.sas.configuration.config.logging.level+json;version=1",
			"services": ["identities", "compute"],
			"createdBy": "sas.identities"
		},
		"links": [{"rel": "self"}],
		"name": "com.sas",
		"level": "DEBUG",
		"limit": 12345678901234567890,
		"ratio": 0.10,
		"enabled": true
	}`

	var configuration viya.Configuration
	if err := json.Unmarshal([]byte(input), &configuration); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if configuration.ID != "abc" || !configuration.Metadata.IsDefault || len(configuration.Metadata.Services) != 2 {
		t.Errorf("decoded configuration = %+v", configuration)
	}
	if _, isProperty := configuration.Properties["links"]; isProperty {
		t.Error("links was decoded as a property")
	}
	if len(configuration.Properties) != 5 {
		t.Errorf("properties = %v, want the 5 that are not reserved fields", configuration.Properties)
	}

	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decoding the encoded configuration: %v", err)
	}
	// Numbers must be written back exactly as they were read.
	if got := string(fields["limit"]); got != "12345678901234567890" {
		t.Errorf("limit was written back as %s", got)
	}
	if got := string(fields["ratio"]); got != "0.10" {
		t.Errorf("ratio was written back as %s", got)
	}
	if _, sent := fields["links"]; sent {
		t.Error("links must not be sent back")
	}
	var metadata map[string]any
	if err := json.Unmarshal(fields["metadata"], &metadata); err != nil {
		t.Fatalf("decoding metadata: %v", err)
	}
	// Metadata that the client does not interpret survives the round trip.
	if metadata["createdBy"] != "sas.identities" {
		t.Errorf("metadata = %v, want createdBy preserved", metadata)
	}
}

func TestConfigurations_lifecycle(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := context.Background()

	mediaType, err := client.DefinitionMediaType(ctx, loggingDefinition)
	if err != nil {
		t.Fatalf("DefinitionMediaType: %v", err)
	}

	created, err := client.CreateConfiguration(ctx, viya.Configuration{
		Metadata: viya.ConfigurationMetadata{
			MediaType: mediaType,
			Services:  []string{"identities"},
		},
		Properties: map[string]any{"name": "com.sas.test", "level": "DEBUG"},
	})
	if err != nil {
		t.Fatalf("CreateConfiguration: %v", err)
	}
	if created.ID == "" || created.ETag == "" {
		t.Fatalf("created configuration has ID %q and ETag %q", created.ID, created.ETag)
	}

	listed, err := client.ListConfigurations(ctx, loggingDefinition)
	if err != nil {
		t.Fatalf("ListConfigurations: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("ListConfigurations = %+v, want only the created instance", listed)
	}

	read, err := client.GetConfiguration(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetConfiguration: %v", err)
	}
	if read.DefinitionName() != loggingDefinition || read.Properties["level"] != "DEBUG" {
		t.Errorf("GetConfiguration = %+v", read)
	}

	read.Properties["level"] = "INFO"
	updated, err := client.UpdateConfiguration(ctx, *read)
	if err != nil {
		t.Fatalf("UpdateConfiguration: %v", err)
	}
	if updated.Properties["level"] != "INFO" || updated.Properties["name"] != "com.sas.test" {
		t.Errorf("UpdateConfiguration = %+v", updated.Properties)
	}

	// The entity tag read before the update is now stale.
	read.Properties["level"] = "TRACE"
	if _, err := client.UpdateConfiguration(ctx, *read); !viya.IsPreconditionFailed(err) {
		t.Errorf("UpdateConfiguration with a stale ETag: error = %v, want precondition failed", err)
	}

	// Without an entity tag, Viya answers with an empty 204: the instance
	// as sent must come back, with its ID.
	read.ETag = ""
	unconditional, err := client.UpdateConfiguration(ctx, *read)
	if err != nil {
		t.Fatalf("unconditional UpdateConfiguration: %v", err)
	}
	if unconditional.ID != created.ID || unconditional.Properties["level"] != "TRACE" {
		t.Errorf("unconditional UpdateConfiguration = %+v, want the instance as sent", unconditional)
	}

	if err := client.DeleteConfiguration(ctx, created.ID); err != nil {
		t.Fatalf("DeleteConfiguration: %v", err)
	}
	if _, err := client.GetConfiguration(ctx, created.ID); !viya.IsNotFound(err) {
		t.Errorf("GetConfiguration after deletion: error = %v, want not found", err)
	}
}

func TestConfigurations_listFollowsPagination(t *testing.T) {
	t.Parallel()
	client, mock := newTestClient(t)

	const total = 130
	for range total {
		mock.AddConfiguration(viyamock.Configuration{
			Definition: loggingDefinition,
			Services:   []string{"identities"},
			Properties: map[string]any{"name": "com.sas", "level": "INFO"},
		})
	}

	listed, err := client.ListConfigurations(context.Background(), loggingDefinition)
	if err != nil {
		t.Fatalf("ListConfigurations: %v", err)
	}
	if len(listed) != total {
		t.Errorf("ListConfigurations returned %d instances, want %d", len(listed), total)
	}
}

func TestDefinitionMediaType(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := context.Background()

	mediaType, err := client.DefinitionMediaType(ctx, loggingDefinition)
	if err != nil {
		t.Fatalf("DefinitionMediaType: %v", err)
	}
	if mediaType != loggingMediaType {
		t.Errorf("DefinitionMediaType = %q, want %q", mediaType, loggingMediaType)
	}

	// A name that is a prefix of another must not match it.
	if _, err := client.DefinitionMediaType(ctx, "logging"); !errors.Is(err, viya.ErrDefinitionNotFound) {
		t.Errorf("DefinitionMediaType for a prefix: error = %v, want ErrDefinitionNotFound", err)
	}
	if _, err := client.DefinitionMediaType(ctx, "no.such.definition"); !errors.Is(err, viya.ErrDefinitionNotFound) {
		t.Errorf("DefinitionMediaType for an unknown name: error = %v, want ErrDefinitionNotFound", err)
	}
}

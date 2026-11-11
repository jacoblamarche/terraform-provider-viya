package viya

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const (
	configurationsPath = "/configuration/configurations"
	definitionsPath    = "/configuration/definitions"

	configurationMediaTypePrefix = "application/vnd.sas.configuration.config."
	configurationMediaTypeSuffix = "+json"
)

// Reserved top-level fields of a configuration instance. Every other field
// is a property of the instance.
const (
	configurationFieldID       = "id"
	configurationFieldMetadata = "metadata"
	configurationFieldLinks    = "links"
)

// Configuration is an instance of a configuration definition of the Viya
// Configuration service: a set of properties that applies to some services.
type Configuration struct {
	ID       string
	Metadata ConfigurationMetadata
	// Properties holds the values of the instance, keyed by property name.
	// Values are decoded from JSON as string, json.Number, bool, nil,
	// []any or map[string]any.
	Properties map[string]any
	// ETag is the entity tag of the representation that was read. It is
	// filled from the ETag header, not from the JSON body.
	ETag string
}

// ConfigurationMetadata describes a configuration instance.
type ConfigurationMetadata struct {
	// MediaType identifies the definition and its version, for example
	// application/vnd.sas.configuration.config.logging.level+json;version=1.
	MediaType string
	Services  []string
	// IsDefault is true while the instance holds the defaults written by a
	// service. A service overwrites such an instance when it starts, so an
	// instance that must keep its values has IsDefault set to false.
	IsDefault bool
	// Extra holds the metadata fields that the client does not interpret,
	// such as createdBy, so that they survive a read-modify-write cycle.
	Extra map[string]any
}

// DefinitionName returns the name of the definition that the instance
// belongs to, taken from its media type. It is empty when the media type is
// not that of a configuration instance.
func (c Configuration) DefinitionName() string {
	return definitionNameFromMediaType(c.Metadata.MediaType)
}

func definitionNameFromMediaType(mediaType string) string {
	base, _, _ := strings.Cut(mediaType, ";")
	name, found := strings.CutPrefix(strings.TrimSpace(base), configurationMediaTypePrefix)
	if !found {
		return ""
	}
	name, found = strings.CutSuffix(name, configurationMediaTypeSuffix)
	if !found {
		return ""
	}
	return name
}

// MarshalJSON flattens the instance into the shape that Viya expects: the
// properties are top-level fields next to id and metadata.
func (c Configuration) MarshalJSON() ([]byte, error) {
	fields := make(map[string]any, len(c.Properties)+2)
	for name, value := range c.Properties {
		fields[name] = value
	}

	metadata := make(map[string]any, len(c.Metadata.Extra)+3)
	for name, value := range c.Metadata.Extra {
		metadata[name] = value
	}
	metadata["mediaType"] = c.Metadata.MediaType
	metadata["isDefault"] = c.Metadata.IsDefault
	services := c.Metadata.Services
	if services == nil {
		services = []string{}
	}
	metadata["services"] = services

	fields[configurationFieldMetadata] = metadata
	if c.ID != "" {
		fields[configurationFieldID] = c.ID
	}
	return json.Marshal(fields)
}

// UnmarshalJSON is the inverse of MarshalJSON. Numbers are kept as
// json.Number so that they are written back exactly as they were read.
func (c *Configuration) UnmarshalJSON(data []byte) error {
	var fields map[string]any
	if err := decodeJSONUseNumber(data, &fields); err != nil {
		return err
	}

	*c = Configuration{Properties: make(map[string]any, len(fields))}
	for name, value := range fields {
		switch name {
		case configurationFieldID:
			c.ID, _ = value.(string)
		case configurationFieldLinks:
			// Links are derived by the service and never sent back.
		case configurationFieldMetadata:
			metadata, _ := value.(map[string]any)
			c.Metadata = configurationMetadataFromMap(metadata)
		default:
			c.Properties[name] = value
		}
	}
	return nil
}

func configurationMetadataFromMap(fields map[string]any) ConfigurationMetadata {
	metadata := ConfigurationMetadata{}
	for name, value := range fields {
		switch name {
		case "mediaType":
			metadata.MediaType, _ = value.(string)
		case "isDefault":
			metadata.IsDefault, _ = value.(bool)
		case "services":
			items, _ := value.([]any)
			for _, item := range items {
				if service, ok := item.(string); ok {
					metadata.Services = append(metadata.Services, service)
				}
			}
		default:
			if metadata.Extra == nil {
				metadata.Extra = make(map[string]any)
			}
			metadata.Extra[name] = value
		}
	}
	return metadata
}

func decodeJSONUseNumber(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(out)
}

type configurationCollection struct {
	Count int             `json:"count"`
	Items []Configuration `json:"items"`
}

// ListConfigurations returns every instance of a definition.
func (c *Client) ListConfigurations(ctx context.Context, definition string) ([]Configuration, error) {
	if definition == "" {
		return nil, errors.New("viya: configuration definition name is required")
	}
	const pageSize = 100

	var configurations []Configuration
	for start := 0; ; start += pageSize {
		var page configurationCollection
		_, err := c.do(ctx, request{
			method: http.MethodGet,
			path:   configurationsPath,
			query: url.Values{
				"definitionName": {definition},
				"start":          {strconv.Itoa(start)},
				"limit":          {strconv.Itoa(pageSize)},
			},
			accept: mediaTypeCollection,
		}, &page)
		if err != nil {
			return nil, err
		}

		configurations = append(configurations, page.Items...)
		if len(page.Items) == 0 || len(configurations) >= page.Count {
			return configurations, nil
		}
	}
}

// GetConfiguration returns the configuration instance with the given ID.
// Use IsNotFound to detect that it does not exist.
func (c *Client) GetConfiguration(ctx context.Context, id string) (*Configuration, error) {
	if id == "" {
		return nil, errors.New("viya: configuration ID is required")
	}

	var configuration Configuration
	header, err := c.do(ctx, request{method: http.MethodGet, path: configurationPath(id)}, &configuration)
	if err != nil {
		return nil, err
	}
	configuration.ETag = header.Get("ETag")
	return &configuration, nil
}

// CreateConfiguration creates a configuration instance. The media type in
// its metadata selects the definition.
func (c *Client) CreateConfiguration(ctx context.Context, configuration Configuration) (*Configuration, error) {
	if configuration.DefinitionName() == "" {
		return nil, fmt.Errorf("viya: %q is not the media type of a configuration instance", configuration.Metadata.MediaType)
	}
	configuration.ID = ""

	var created Configuration
	header, err := c.do(ctx, request{
		method:      http.MethodPost,
		path:        configurationsPath,
		body:        configuration,
		contentType: configuration.Metadata.MediaType,
	}, &created)
	if err != nil {
		return nil, err
	}
	if created.ID == "" {
		// The service answered without a body: the Location header still
		// names the new instance.
		created = configuration
		created.ID = path.Base(header.Get("Location"))
		if created.ID == "." || created.ID == "/" {
			return nil, errors.New("viya: the created configuration has no ID")
		}
	}
	created.ETag = header.Get("ETag")
	return &created, nil
}

// UpdateConfiguration replaces the configuration instance identified by
// configuration.ID. Properties that are not in configuration.Properties are
// removed, so callers normally read the instance, change it, and write it
// back.
//
// When configuration.ETag is set, it is sent as an If-Match precondition:
// the update fails with an error for which IsPreconditionFailed is true if
// the instance changed since that entity tag was read.
func (c *Client) UpdateConfiguration(ctx context.Context, configuration Configuration) (*Configuration, error) {
	if configuration.ID == "" {
		return nil, errors.New("viya: configuration ID is required")
	}
	if configuration.DefinitionName() == "" {
		return nil, fmt.Errorf("viya: %q is not the media type of a configuration instance", configuration.Metadata.MediaType)
	}

	var updated Configuration
	header, err := c.do(ctx, request{
		method:      http.MethodPut,
		path:        configurationPath(configuration.ID),
		body:        configuration,
		contentType: configuration.Metadata.MediaType,
		ifMatch:     configuration.ETag,
	}, &updated)
	if err != nil {
		return nil, err
	}
	if updated.ID == "" {
		// An unconditional update is answered with an empty 204.
		updated = configuration
	}
	updated.ETag = header.Get("ETag")
	return &updated, nil
}

// DeleteConfiguration deletes the configuration instance with the given ID.
// Use IsNotFound to detect that it was already gone.
func (c *Client) DeleteConfiguration(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("viya: configuration ID is required")
	}
	_, err := c.do(ctx, request{method: http.MethodDelete, path: configurationPath(id)}, nil)
	return err
}

// ErrDefinitionNotFound is returned by DefinitionMediaType when the
// deployment has no definition with the given name.
var ErrDefinitionNotFound = errors.New("viya: configuration definition not found")

type definitionCollection struct {
	Count int `json:"count"`
	Items []struct {
		Name  string `json:"name"`
		Links []struct {
			Type             string `json:"type"`
			ItemType         string `json:"itemType"`
			ResponseItemType string `json:"responseItemType"`
		} `json:"links"`
	} `json:"items"`
}

// DefinitionMediaType returns the media type of the instances of a
// definition, which carries its version.
//
// The Configuration service cannot read a definition by name and ignores
// filters on the definition list, so the whole list is searched. The media
// type is taken from the links of the definition.
func (c *Client) DefinitionMediaType(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", errors.New("viya: configuration definition name is required")
	}
	const pageSize = 1000
	prefix := configurationMediaTypePrefix + name + configurationMediaTypeSuffix

	seen := 0
	for start := 0; ; start += pageSize {
		var page definitionCollection
		_, err := c.do(ctx, request{
			method: http.MethodGet,
			path:   definitionsPath,
			query: url.Values{
				"start": {strconv.Itoa(start)},
				"limit": {strconv.Itoa(pageSize)},
			},
			accept: mediaTypeCollection,
		}, &page)
		if err != nil {
			return "", err
		}

		for _, item := range page.Items {
			if item.Name != name {
				continue
			}
			for _, link := range item.Links {
				for _, candidate := range []string{link.ResponseItemType, link.ItemType, link.Type} {
					if strings.HasPrefix(candidate, prefix) {
						return candidate, nil
					}
				}
			}
			return "", fmt.Errorf("viya: definition %q does not name the media type of its instances", name)
		}

		seen += len(page.Items)
		if len(page.Items) == 0 || seen >= page.Count {
			return "", fmt.Errorf("%w: %q", ErrDefinitionNotFound, name)
		}
	}
}

func configurationPath(id string) string {
	return configurationsPath + "/" + url.PathEscape(id)
}

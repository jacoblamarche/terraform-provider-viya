package viyamock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	configurationsPath = "/configuration/configurations"

	configurationMediaTypePrefix = "application/vnd.sas.configuration.config."
	configurationMediaTypeSuffix = "+json"

	// SecretMask is what the fake returns in place of the value of a secret
	// property. Writing it back leaves the stored secret unchanged.
	SecretMask = "********"
)

// ConfigurationProperty describes one property of a definition.
type ConfigurationProperty struct {
	Name string `json:"name"`
	// Type is informative: string, number or boolean.
	Type string `json:"type"`
	// Secret makes the fake mask the value when the instance is read.
	Secret bool `json:"-"`
}

// ConfigurationDefinition describes the instances that a definition allows.
type ConfigurationDefinition struct {
	Name       string                  `json:"name"`
	Version    int                     `json:"version"`
	Properties []ConfigurationProperty `json:"properties"`
}

// Configuration is a configuration instance held by the fake.
type Configuration struct {
	ID         string
	Definition string
	Services   []string
	IsDefault  bool
	// Properties holds the stored values, secrets included and unmasked.
	Properties map[string]any

	etag      string
	createdBy string
	created   time.Time
	modified  time.Time
}

// sampleDefinitions is the catalogue that every fake starts with. It is a
// small subset of what a real deployment has, chosen to cover the shapes
// that matter: several named instances per service, a single instance with
// a secret, and many small instances.
func sampleDefinitions() []ConfigurationDefinition {
	return []ConfigurationDefinition{
		{
			Name: "sas.compute.server", Version: 1,
			Properties: []ConfigurationProperty{
				{Name: "name", Type: "string"},
				{Name: "contents", Type: "string"},
			},
		},
		{
			Name: "sas.identities.providers.ldap.connection", Version: 1,
			Properties: []ConfigurationProperty{
				{Name: "host", Type: "string"},
				{Name: "port", Type: "number"},
				{Name: "userDN", Type: "string"},
				{Name: "password", Type: "string", Secret: true},
				{Name: "anonymousBind", Type: "boolean"},
			},
		},
		{
			Name: "logging.level", Version: 1,
			Properties: []ConfigurationProperty{
				{Name: "name", Type: "string"},
				{Name: "level", Type: "string"},
			},
		},
	}
}

func (s *Server) seedSampleConfigurations() {
	for _, definition := range sampleDefinitions() {
		s.definitions[definition.Name] = definition
	}
	for _, name := range []string{"autoexec_code", "configuration_options", "startup_commands"} {
		s.AddConfiguration(Configuration{
			Definition: "sas.compute.server",
			Services:   []string{"compute"},
			IsDefault:  true,
			Properties: map[string]any{"name": name, "contents": ""},
		})
	}
	s.AddConfiguration(Configuration{
		Definition: "sas.identities.providers.ldap.connection",
		Services:   []string{"identities"},
		IsDefault:  true,
		Properties: map[string]any{
			"host":          "ldap.example.com",
			"port":          json.Number("389"),
			"userDN":        "cn=viya,ou=service,dc=example,dc=com",
			"password":      "initial-ldap-password",
			"anonymousBind": false,
		},
	})
}

// AddConfiguration stores a configuration instance without going through
// the API, as a service does at startup, and returns its ID.
func (s *Server) AddConfiguration(configuration Configuration) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	stored := configuration
	stored.ID = newUUID()
	stored.Services = slices.Clone(configuration.Services)
	stored.Properties = maps.Clone(configuration.Properties)
	stored.etag = s.nextETag()
	stored.createdBy = "sas." + strings.Join(stored.Services, ".")
	stored.created, stored.modified = now, now
	s.configurations[stored.ID] = &stored
	return stored.ID
}

// Configuration returns a copy of the instance with the given ID, with its
// secrets unmasked, if it exists.
func (s *Server) Configuration(id string) (Configuration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	configuration, ok := s.configurations[id]
	if !ok {
		return Configuration{}, false
	}
	return configuration.clone(), true
}

// FindConfigurations returns copies of the instances of a definition, in a
// stable order.
func (s *Server) FindConfigurations(definition string) []Configuration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findConfigurations(definition)
}

// findConfigurations is FindConfigurations for callers that hold s.mu. An
// empty definition matches every instance.
func (s *Server) findConfigurations(definition string) []Configuration {
	var found []Configuration
	for _, configuration := range s.configurations {
		if definition == "" || configuration.Definition == definition {
			found = append(found, configuration.clone())
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].created.Equal(found[j].created) {
			return found[i].created.Before(found[j].created)
		}
		return found[i].ID < found[j].ID
	})
	return found
}

// SetConfigurationProperty changes one property without going through the
// API, to simulate a change made outside Terraform. It reports whether the
// instance exists.
func (s *Server) SetConfigurationProperty(id, name string, value any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	configuration, ok := s.configurations[id]
	if !ok {
		return false
	}
	configuration.Properties[name] = value
	configuration.modified = s.now().UTC()
	configuration.etag = s.nextETag()
	return true
}

// RemoveConfiguration deletes an instance without going through the API. It
// reports whether the instance existed.
func (s *Server) RemoveConfiguration(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.configurations[id]
	delete(s.configurations, id)
	return ok
}

func (c *Configuration) clone() Configuration {
	copied := *c
	copied.Services = slices.Clone(c.Services)
	copied.Properties = maps.Clone(c.Properties)
	return copied
}

// representation renders the instance as the API returns it: properties as
// top-level fields, secrets masked.
func (s *Server) configurationRepresentation(c Configuration) map[string]any {
	fields := make(map[string]any, len(c.Properties)+3)
	for name, value := range c.Properties {
		if s.isSecret(c.Definition, name) {
			value = SecretMask
		}
		fields[name] = value
	}

	href := configurationsPath + "/" + url.PathEscape(c.ID)
	fields["id"] = c.ID
	fields["metadata"] = map[string]any{
		"isDefault":         c.IsDefault,
		"mediaType":         s.configurationMediaType(c.Definition),
		"services":          c.Services,
		"createdBy":         c.createdBy,
		"creationTimeStamp": c.created.Format(timestampLayout),
		"modifiedBy":        c.createdBy,
		"modifiedTimeStamp": c.modified.Format(timestampLayout),
	}
	fields["links"] = []link{
		{Method: http.MethodGet, Rel: "self", Href: href, URI: href},
		{Method: http.MethodPut, Rel: "update", Href: href, URI: href},
		{Method: http.MethodDelete, Rel: "delete", Href: href, URI: href},
	}
	return fields
}

// isSecret must be called with s.mu held.
func (s *Server) isSecret(definition, property string) bool {
	for _, candidate := range s.definitions[definition].Properties {
		if candidate.Name == property {
			return candidate.Secret
		}
	}
	return false
}

func (s *Server) configurationMediaType(definition string) string {
	return fmt.Sprintf("%s%s%s;version=%d",
		configurationMediaTypePrefix, definition, configurationMediaTypeSuffix, s.definitions[definition].Version)
}

func (s *Server) handleGetDefinition(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	definition, ok := s.definitions[r.PathValue("name")]
	s.mu.Unlock()
	if !ok {
		writeError(w, r, http.StatusNotFound,
			fmt.Sprintf("The configuration definition %q could not be found.", r.PathValue("name")))
		return
	}
	writeJSON(w, http.StatusOK, "application/vnd.sas.configuration.definition+json", definition)
}

func (s *Server) handleListConfigurations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	for name := range query {
		switch name {
		case "definitionName", "start", "limit":
		default:
			writeError(w, r, http.StatusBadRequest,
				fmt.Sprintf("The mock does not implement the %q query parameter.", name))
			return
		}
	}
	start, err := queryInt(query, "start", 0)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := queryInt(query, "limit", defaultPageSize)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	all := s.findConfigurations(query.Get("definitionName"))
	items := make([]map[string]any, 0, len(all))
	for _, configuration := range all[min(start, len(all)):min(start+limit, len(all))] {
		items = append(items, s.configurationRepresentation(configuration))
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, mediaTypeCollection, map[string]any{
		"name":    "configurations",
		"accept":  "application/vnd.sas.configuration.config+json",
		"start":   start,
		"limit":   limit,
		"count":   len(all),
		"items":   items,
		"links":   []link{},
		"version": 2,
	})
}

func (s *Server) handleCreateConfiguration(w http.ResponseWriter, r *http.Request) {
	input, ok := s.readConfigurationInput(w, r)
	if !ok {
		return
	}

	s.mu.Lock()
	now := s.now().UTC()
	configuration := &Configuration{
		ID:         newUUID(),
		Definition: input.definition,
		Services:   input.services,
		IsDefault:  input.isDefault,
		Properties: input.properties,
		etag:       s.nextETag(),
		createdBy:  "viya-mock-user",
		created:    now,
		modified:   now,
	}
	s.configurations[configuration.ID] = configuration
	body := s.configurationRepresentation(*configuration)
	etag := configuration.etag
	s.mu.Unlock()

	w.Header().Set("Location", configurationsPath+"/"+configuration.ID)
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusCreated, s.configurationMediaTypeLocked(input.definition), body)
}

func (s *Server) handleGetConfiguration(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	configuration, ok := s.configurations[r.PathValue("id")]
	if !ok {
		s.mu.Unlock()
		writeConfigurationNotFound(w, r)
		return
	}
	body := s.configurationRepresentation(*configuration)
	etag, mediaType := configuration.etag, s.configurationMediaType(configuration.Definition)
	s.mu.Unlock()

	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, mediaType, body)
}

func (s *Server) handleUpdateConfiguration(w http.ResponseWriter, r *http.Request) {
	input, ok := s.readConfigurationInput(w, r)
	if !ok {
		return
	}
	ifMatch := r.Header.Get("If-Match")

	s.mu.Lock()
	configuration, exists := s.configurations[r.PathValue("id")]
	switch {
	case !exists:
		s.mu.Unlock()
		writeConfigurationNotFound(w, r)
		return
	case input.definition != configuration.Definition:
		s.mu.Unlock()
		writeError(w, r, http.StatusBadRequest, "The media type of a configuration cannot be changed.")
		return
	case ifMatch == "" && s.requireIfMatch:
		s.mu.Unlock()
		writeError(w, r, http.StatusPreconditionRequired, "The If-Match header is required.")
		return
	case ifMatch != "" && ifMatch != "*" && ifMatch != configuration.etag:
		s.mu.Unlock()
		writeError(w, r, http.StatusPreconditionFailed,
			"The If-Match header does not match the current entity tag of the configuration.")
		return
	}

	// A secret that comes back masked was not changed by the client.
	for name, value := range input.properties {
		if value == SecretMask && s.isSecret(configuration.Definition, name) {
			input.properties[name] = configuration.Properties[name]
		}
	}
	configuration.Services = input.services
	configuration.IsDefault = input.isDefault
	configuration.Properties = input.properties
	configuration.modified = s.now().UTC()
	configuration.etag = s.nextETag()

	body := s.configurationRepresentation(*configuration)
	etag, mediaType := configuration.etag, s.configurationMediaType(configuration.Definition)
	s.mu.Unlock()

	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, mediaType, body)
}

func (s *Server) handleDeleteConfiguration(w http.ResponseWriter, r *http.Request) {
	if !s.RemoveConfiguration(r.PathValue("id")) {
		writeConfigurationNotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// configurationMediaTypeLocked is configurationMediaType for callers that
// do not hold s.mu.
func (s *Server) configurationMediaTypeLocked(definition string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configurationMediaType(definition)
}

type configurationInput struct {
	definition string
	services   []string
	isDefault  bool
	properties map[string]any
}

// readConfigurationInput writes the error response itself when it returns
// false.
func (s *Server) readConfigurationInput(w http.ResponseWriter, r *http.Request) (configurationInput, bool) {
	fail := func(message string) (configurationInput, bool) {
		writeError(w, r, http.StatusBadRequest, message)
		return configurationInput{}, false
	}

	// The definition is named by the Content-Type of the request.
	definition := definitionFromContentType(r.Header.Get("Content-Type"))
	s.mu.Lock()
	_, known := s.definitions[definition]
	s.mu.Unlock()
	if !known {
		return fail(fmt.Sprintf("The Content-Type %q does not name a known configuration definition.",
			r.Header.Get("Content-Type")))
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		return fail("The request body could not be read.")
	}
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return fail("The request body is not a valid configuration.")
	}

	input := configurationInput{definition: definition, properties: make(map[string]any, len(fields))}
	for name, value := range fields {
		switch name {
		case "id", "links":
		case "metadata":
			metadata, _ := value.(map[string]any)
			input.isDefault, _ = metadata["isDefault"].(bool)
			services, _ := metadata["services"].([]any)
			for _, service := range services {
				if text, ok := service.(string); ok {
					input.services = append(input.services, text)
				}
			}
			if mediaType, ok := metadata["mediaType"].(string); ok && definitionFromContentType(mediaType) != definition {
				return fail("The media type in the metadata does not match the Content-Type of the request.")
			}
		default:
			input.properties[name] = value
		}
	}
	return input, true
}

// definitionFromContentType extracts the definition name from a media type
// such as application/vnd.sas.configuration.config.logging.level+json;version=1.
func definitionFromContentType(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	name, found := strings.CutPrefix(mediaType, configurationMediaTypePrefix)
	if !found {
		return ""
	}
	name, found = strings.CutSuffix(name, configurationMediaTypeSuffix)
	if !found {
		return ""
	}
	return name
}

func writeConfigurationNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound,
		fmt.Sprintf("The configuration %q could not be found.", r.PathValue("id")))
}

func newUUID() string {
	raw := randomHex(16)
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32]
}

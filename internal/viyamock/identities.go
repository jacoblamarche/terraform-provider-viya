package viyamock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	mediaTypeGroup      = "application/vnd.sas.identity.group+json"
	mediaTypeCollection = "application/vnd.sas.collection+json"

	groupsPath = "/identities/groups"

	defaultPageSize = 20

	timestampLayout = "2006-01-02T15:04:05.000Z"

	maxBodyBytes = 1 << 20
)

// Group is a custom group held by the fake Identities service.
type Group struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	State             string `json:"state"`
	ProviderID        string `json:"providerId"`
	CreationTimeStamp string `json:"creationTimeStamp"`
	ModifiedTimeStamp string `json:"modifiedTimeStamp"`
	Links             []link `json:"links"`
	Version           int    `json:"version"`

	etag     string
	modified time.Time
}

// groupInput is the part of a group that a client may set.
type groupInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	State       string `json:"state"`
}

// Group returns a copy of the group with the given ID, if it exists.
func (s *Server) Group(id string) (Group, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	group, ok := s.groups[id]
	if !ok {
		return Group{}, false
	}
	return *group, true
}

// RemoveGroup deletes a group without going through the API, to simulate a
// change made outside Terraform. It reports whether the group existed.
func (s *Server) RemoveGroup(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.groups[id]
	delete(s.groups, id)
	return ok
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	input, ok := readGroupInput(w, r)
	if !ok {
		return
	}
	if input.ID == "" {
		writeError(w, r, http.StatusBadRequest, "The group ID is required.")
		return
	}
	if !validateGroupInput(w, r, &input) {
		return
	}

	s.mu.Lock()
	if _, exists := s.groups[input.ID]; exists {
		s.mu.Unlock()
		writeError(w, r, http.StatusConflict,
			fmt.Sprintf("A group with the ID %q already exists.", input.ID))
		return
	}
	now := s.now().UTC()
	group := &Group{
		ID:                input.ID,
		Name:              input.Name,
		Description:       input.Description,
		State:             input.State,
		ProviderID:        "local",
		CreationTimeStamp: now.Format(timestampLayout),
		ModifiedTimeStamp: now.Format(timestampLayout),
		Links:             groupLinks(input.ID),
		Version:           2,
		etag:              s.nextETag(),
		modified:          now,
	}
	s.groups[group.ID] = group
	created := *group
	s.mu.Unlock()

	w.Header().Set("Location", groupsPath+"/"+url.PathEscape(created.ID))
	writeGroup(w, http.StatusCreated, created)
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	group, ok := s.Group(r.PathValue("id"))
	if !ok {
		writeGroupNotFound(w, r)
		return
	}
	writeGroup(w, http.StatusOK, group)
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	input, ok := readGroupInput(w, r)
	if !ok {
		return
	}
	if input.ID != "" && input.ID != id {
		writeError(w, r, http.StatusBadRequest, "The group ID in the body does not match the ID in the path.")
		return
	}
	if !validateGroupInput(w, r, &input) {
		return
	}
	ifMatch := r.Header.Get("If-Match")

	s.mu.Lock()
	group, exists := s.groups[id]
	switch {
	case !exists:
		s.mu.Unlock()
		writeGroupNotFound(w, r)
		return
	case ifMatch == "" && s.requireIfMatch:
		s.mu.Unlock()
		writeError(w, r, http.StatusPreconditionRequired, "The If-Match header is required.")
		return
	case ifMatch != "" && ifMatch != "*" && ifMatch != group.etag:
		s.mu.Unlock()
		writeError(w, r, http.StatusPreconditionFailed,
			"The If-Match header does not match the current entity tag of the group.")
		return
	}
	now := s.now().UTC()
	group.Name = input.Name
	group.Description = input.Description
	group.State = input.State
	group.ModifiedTimeStamp = now.Format(timestampLayout)
	group.modified = now
	group.etag = s.nextETag()
	updated := *group
	s.mu.Unlock()

	writeGroup(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if !s.RemoveGroup(r.PathValue("id")) {
		writeGroupNotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readGroupInput writes the error response itself when it returns false.
func readGroupInput(w http.ResponseWriter, r *http.Request) (groupInput, bool) {
	var input groupInput
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err == nil {
		err = json.Unmarshal(body, &input)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "The request body is not a valid group.")
		return groupInput{}, false
	}
	return input, true
}

// validateGroupInput applies defaults, and writes the error response itself
// when it returns false.
func validateGroupInput(w http.ResponseWriter, r *http.Request, input *groupInput) bool {
	if input.Name == "" {
		writeError(w, r, http.StatusBadRequest, "The group name is required.")
		return false
	}
	switch input.State {
	case "":
		input.State = "active"
	case "active", "inactive":
	default:
		writeError(w, r, http.StatusBadRequest,
			fmt.Sprintf("The group state %q is not valid: it must be active or inactive.", input.State))
		return false
	}
	return true
}

func writeGroup(w http.ResponseWriter, status int, group Group) {
	w.Header().Set("ETag", group.etag)
	w.Header().Set("Last-Modified", group.modified.Format(http.TimeFormat))
	writeJSON(w, status, mediaTypeGroup, group)
}

func writeGroupNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound,
		fmt.Sprintf("The group %q could not be found.", r.PathValue("id")))
}

func groupLinks(id string) []link {
	href := groupsPath + "/" + url.PathEscape(id)
	return []link{
		{Method: http.MethodGet, Rel: "self", Href: href, URI: href, Type: "application/vnd.sas.identity.group"},
		{Method: http.MethodPut, Rel: "update", Href: href, URI: href, Type: "application/vnd.sas.identity.group"},
		{Method: http.MethodDelete, Rel: "delete", Href: href, URI: href},
	}
}

func queryInt(query url.Values, name string, fallback int) (int, error) {
	raw := query.Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("the %q query parameter must be a non-negative integer", name)
	}
	return value, nil
}

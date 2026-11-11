package viya

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

const (
	groupsPath = "/identities/groups"

	mediaTypeGroup      = "application/vnd.sas.identity.group+json"
	mediaTypeCollection = "application/vnd.sas.collection+json"
)

// States of a group.
const (
	GroupStateActive   = "active"
	GroupStateInactive = "inactive"
)

// Group is a group of the Viya Identities service.
type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	State       string `json:"state,omitempty"`
	// ProviderID is "local" for custom groups. It is set by the service.
	ProviderID string `json:"providerId,omitempty"`
	// ETag is the entity tag of the representation that was read. It is not
	// part of the JSON body: the client fills it from the ETag header.
	ETag string `json:"-"`
}

// CreateGroup creates a custom group and returns it as stored by Viya.
func (c *Client) CreateGroup(ctx context.Context, group Group) (*Group, error) {
	if group.ID == "" {
		return nil, errors.New("viya: group ID is required")
	}

	var created Group
	header, err := c.do(ctx, request{
		method:      http.MethodPost,
		path:        groupsPath,
		body:        group,
		contentType: mediaTypeGroup,
		accept:      mediaTypeGroup,
	}, &created)
	if err != nil {
		return nil, err
	}
	created.ETag = header.Get("ETag")
	return &created, nil
}

// GetGroup returns the group with the given ID. Use IsNotFound to detect
// that it does not exist.
func (c *Client) GetGroup(ctx context.Context, id string) (*Group, error) {
	if id == "" {
		return nil, errors.New("viya: group ID is required")
	}

	var group Group
	header, err := c.do(ctx, request{
		method: http.MethodGet,
		path:   groupPath(id),
		accept: mediaTypeGroup,
	}, &group)
	if err != nil {
		return nil, err
	}
	group.ETag = header.Get("ETag")
	return &group, nil
}

// UpdateGroup replaces the group identified by group.ID.
//
// When group.ETag is set, it is sent as an If-Match precondition: the update
// fails with an error for which IsPreconditionFailed is true if the group
// was changed since that entity tag was read.
func (c *Client) UpdateGroup(ctx context.Context, group Group) (*Group, error) {
	if group.ID == "" {
		return nil, errors.New("viya: group ID is required")
	}

	var updated Group
	header, err := c.do(ctx, request{
		method:      http.MethodPut,
		path:        groupPath(group.ID),
		body:        group,
		contentType: mediaTypeGroup,
		accept:      mediaTypeGroup,
		ifMatch:     group.ETag,
	}, &updated)
	if err != nil {
		return nil, err
	}
	updated.ETag = header.Get("ETag")
	return &updated, nil
}

// DeleteGroup deletes the group with the given ID. Use IsNotFound to detect
// that it was already gone.
func (c *Client) DeleteGroup(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("viya: group ID is required")
	}
	_, err := c.do(ctx, request{method: http.MethodDelete, path: groupPath(id)}, nil)
	return err
}

func groupPath(id string) string {
	return groupsPath + "/" + url.PathEscape(id)
}

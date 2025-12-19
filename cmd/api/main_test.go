package main

import (
	"context"
	"testing"
)

type mockDevicesClient struct {
	devices      []Device
	listErr      error
	setTagsErr   error
	setTagsCalls []struct {
		deviceID string
		tags     []string
	}
}

func (m *mockDevicesClient) List(ctx context.Context) ([]Device, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.devices, nil
}

func (m *mockDevicesClient) SetTags(ctx context.Context, deviceID string, tags []string) error {
	m.setTagsCalls = append(m.setTagsCalls, struct {
		deviceID string
		tags     []string
	}{deviceID, tags})
	return m.setTagsErr
}

func TestGetPendingDevices_ReturnsAuthorizedDevicesWithNoTags(t *testing.T) {
	mock := &mockDevicesClient{
		devices: []Device{
			{ID: "1", Name: "device1", Authorized: true, Tags: []string{}},
		},
	}

	pending, err := getPendingDevices(context.Background(), mock)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending device, got %d", len(pending))
	}
	if pending[0].ID != "1" || pending[0].Name != "device1" {
		t.Errorf("unexpected device: %+v", pending[0])
	}
}

func TestGetPendingDevices_SkipsUnauthorizedDevices(t *testing.T) {
	mock := &mockDevicesClient{
		devices: []Device{
			{ID: "1", Name: "device1", Authorized: false, Tags: []string{}},
		},
	}

	pending, err := getPendingDevices(context.Background(), mock)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending devices, got %d", len(pending))
	}
}

func TestGetPendingDevices_SkipsDevicesWithExistingTags(t *testing.T) {
	mock := &mockDevicesClient{
		devices: []Device{
			{ID: "1", Name: "device1", Authorized: true, Tags: []string{"tag:existing"}},
		},
	}

	pending, err := getPendingDevices(context.Background(), mock)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending devices, got %d", len(pending))
	}
}

func TestGetPendingDevices_FiltersMultipleDevices(t *testing.T) {
	mock := &mockDevicesClient{
		devices: []Device{
			{ID: "1", Name: "authorized-no-tags", Authorized: true, Tags: []string{}},
			{ID: "2", Name: "unauthorized", Authorized: false, Tags: []string{}},
			{ID: "3", Name: "authorized-with-tags", Authorized: true, Tags: []string{"tag:x"}},
			{ID: "4", Name: "authorized-no-tags-2", Authorized: true, Tags: []string{}},
		},
	}

	pending, err := getPendingDevices(context.Background(), mock)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending devices, got %d", len(pending))
	}
	if pending[0].ID != "1" {
		t.Errorf("expected first device ID '1', got %s", pending[0].ID)
	}
	if pending[1].ID != "4" {
		t.Errorf("expected second device ID '4', got %s", pending[1].ID)
	}
}

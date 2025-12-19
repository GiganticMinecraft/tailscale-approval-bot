package main

import (
	"context"
	"errors"
	"testing"
)

type mockDevicesClient struct {
	devices    []Device
	listErr    error
	setTagsErr error
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

func TestReconcile_AppliesTagsToAuthorizedDevicesWithNoTags(t *testing.T) {
	mock := &mockDevicesClient{
		devices: []Device{
			{ID: "1", Name: "device1", Authorized: true, Tags: []string{}},
		},
	}

	reconcile(context.Background(), mock, []string{"tag:test"})

	if len(mock.setTagsCalls) != 1 {
		t.Fatalf("expected 1 SetTags call, got %d", len(mock.setTagsCalls))
	}
	if mock.setTagsCalls[0].deviceID != "1" {
		t.Errorf("expected deviceID '1', got %s", mock.setTagsCalls[0].deviceID)
	}
	if len(mock.setTagsCalls[0].tags) != 1 || mock.setTagsCalls[0].tags[0] != "tag:test" {
		t.Errorf("expected tags [tag:test], got %v", mock.setTagsCalls[0].tags)
	}
}

func TestReconcile_SkipsUnauthorizedDevices(t *testing.T) {
	mock := &mockDevicesClient{
		devices: []Device{
			{ID: "1", Name: "device1", Authorized: false, Tags: []string{}},
		},
	}

	reconcile(context.Background(), mock, []string{"tag:test"})

	if len(mock.setTagsCalls) != 0 {
		t.Fatalf("expected 0 SetTags calls, got %d", len(mock.setTagsCalls))
	}
}

func TestReconcile_SkipsDevicesWithExistingTags(t *testing.T) {
	mock := &mockDevicesClient{
		devices: []Device{
			{ID: "1", Name: "device1", Authorized: true, Tags: []string{"tag:existing"}},
		},
	}

	reconcile(context.Background(), mock, []string{"tag:test"})

	if len(mock.setTagsCalls) != 0 {
		t.Fatalf("expected 0 SetTags calls, got %d", len(mock.setTagsCalls))
	}
}

func TestReconcile_ProcessesMultipleDevices(t *testing.T) {
	mock := &mockDevicesClient{
		devices: []Device{
			{ID: "1", Name: "authorized-no-tags", Authorized: true, Tags: []string{}},
			{ID: "2", Name: "unauthorized", Authorized: false, Tags: []string{}},
			{ID: "3", Name: "authorized-with-tags", Authorized: true, Tags: []string{"tag:x"}},
			{ID: "4", Name: "authorized-no-tags-2", Authorized: true, Tags: []string{}},
		},
	}

	reconcile(context.Background(), mock, []string{"tag:a", "tag:b"})

	if len(mock.setTagsCalls) != 2 {
		t.Fatalf("expected 2 SetTags calls, got %d", len(mock.setTagsCalls))
	}

	if mock.setTagsCalls[0].deviceID != "1" {
		t.Errorf("expected first call for device '1', got %s", mock.setTagsCalls[0].deviceID)
	}
	if mock.setTagsCalls[1].deviceID != "4" {
		t.Errorf("expected second call for device '4', got %s", mock.setTagsCalls[1].deviceID)
	}
}

func TestReconcile_ContinuesOnSetTagsError(t *testing.T) {
	mock := &mockDevicesClient{
		devices: []Device{
			{ID: "1", Name: "device1", Authorized: true, Tags: []string{}},
			{ID: "2", Name: "device2", Authorized: true, Tags: []string{}},
		},
		setTagsErr: errors.New("api error"),
	}

	reconcile(context.Background(), mock, []string{"tag:test"})

	// 5 retries per device × 2 devices = 10 calls
	if len(mock.setTagsCalls) != 10 {
		t.Fatalf("expected 10 SetTags calls (5 retries × 2 devices), got %d", len(mock.setTagsCalls))
	}
}

package cloudsql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sqladmin/v1"
)

func newFakeSQLAdmin(t *testing.T, handler http.HandlerFunc) *sqladmin.Service {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	svc, err := sqladmin.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}

	return svc
}

func failedOperation(name string) *sqladmin.Operation {
	return &sqladmin.Operation{
		Name:   name,
		Status: "DONE",
		Error: &sqladmin.OperationErrors{
			Errors: []*sqladmin.OperationError{{Code: "ERROR_RDBMS", Message: "access denied to bucket"}},
		},
	}
}

func TestWaitForSQLOperationReturnsErrorOfDoneOperation(t *testing.T) {
	svc := newFakeSQLAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s: the operation is already DONE", r.Method, r.URL.Path)
	})

	err := WaitForSQLOperation(context.Background(), svc, "project", failedOperation("op-1"))
	if err == nil || !strings.Contains(err.Error(), "ERROR_RDBMS: access denied to bucket") {
		t.Fatalf("expected the operation error, got %v", err)
	}
}

func TestWaitForSQLOperationPollsUntilDone(t *testing.T) {
	operationPollInterval = time.Millisecond
	t.Cleanup(func() { operationPollInterval = 10 * time.Second })

	tests := []struct {
		name    string
		final   *sqladmin.Operation
		wantErr string
	}{
		{name: "succeeded", final: &sqladmin.Operation{Name: "op-2", Status: "DONE"}},
		{name: "failed", final: failedOperation("op-2"), wantErr: "operation op-2 failed: ERROR_RDBMS: access denied to bucket"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			polls := 0
			svc := newFakeSQLAdmin(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/projects/project/operations/op-2" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				polls++
				op := &sqladmin.Operation{Name: "op-2", Status: "RUNNING"}
				if polls >= 2 {
					op = tt.final
				}
				if err := json.NewEncoder(w).Encode(op); err != nil {
					t.Error(err)
				}
			})

			err := WaitForSQLOperation(context.Background(), svc, "project", &sqladmin.Operation{Name: "op-2", Status: "PENDING"})
			if tt.wantErr == "" && err != nil {
				t.Fatalf("expected success, got %v", err)
			}
			if tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("expected %q, got %v", tt.wantErr, err)
			}
			if polls != 2 {
				t.Fatalf("expected 2 polls, got %d", polls)
			}
		})
	}
}

func TestWaitForSQLOperationStopsOnCancel(t *testing.T) {
	svc := newFakeSQLAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s: the context is already cancelled", r.Method, r.URL.Path)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := WaitForSQLOperation(ctx, svc, "project", &sqladmin.Operation{Name: "op-3", Status: "RUNNING"})
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("expected a cancellation error, got %v", err)
	}
}

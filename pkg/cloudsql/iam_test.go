package cloudsql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"
)

const (
	testBucket = "backups"
	testRole   = "roles/storage.objectAdmin"
	testMember = "serviceAccount:sql@example.iam.gserviceaccount.com"
)

// fakeBucketIAM serves GetIamPolicy with policy and records what SetIamPolicy receives.
func fakeBucketIAM(t *testing.T, policy *storage.Policy) (*storage.Service, *[]*storage.Policy) {
	t.Helper()

	var written []*storage.Policy
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storage/v1/b/"+testBucket+"/iam" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			if got := r.URL.Query().Get("optionsRequestedPolicyVersion"); got != "3" {
				t.Errorf("expected optionsRequestedPolicyVersion=3, got %q", got)
			}
			if err := json.NewEncoder(w).Encode(policy); err != nil {
				t.Error(err)
			}
		case http.MethodPut:
			p := &storage.Policy{}
			if err := json.NewDecoder(r.Body).Decode(p); err != nil {
				t.Error(err)
			}
			written = append(written, p)
			if err := json.NewEncoder(w).Encode(p); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	svc, err := storage.NewService(context.Background(), option.WithEndpoint(server.URL+"/storage/v1/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}

	return svc, &written
}

func TestAddRoleBindingIgnoresConditionalGrant(t *testing.T) {
	svc, written := fakeBucketIAM(t, &storage.Policy{
		Version: 3,
		Bindings: []*storage.PolicyBindings{{
			Role:      testRole,
			Members:   []string{testMember},
			Condition: &storage.Expr{Title: "temporary", Expression: `request.time < timestamp("2020-01-01T00:00:00Z")`},
		}},
	})

	if err := AddRoleBindingToGCSBucket(context.Background(), svc, "project", testBucket, testRole, "sql@example.iam.gserviceaccount.com", "instance"); err != nil {
		t.Fatal(err)
	}

	if len(*written) != 1 {
		t.Fatalf("expected one SetIamPolicy call, got %d", len(*written))
	}
	p := (*written)[0]
	if p.Version != 3 {
		t.Errorf("expected policy version 3 to be written back, got %d", p.Version)
	}
	if len(p.Bindings) != 2 {
		t.Fatalf("expected the conditional binding plus a new one, got %d bindings", len(p.Bindings))
	}
	if p.Bindings[0].Condition == nil {
		t.Error("the conditional binding lost its condition")
	}
	added := p.Bindings[1]
	if added.Role != testRole || added.Condition != nil || len(added.Members) != 1 || added.Members[0] != testMember {
		t.Errorf("unexpected added binding: %+v", added)
	}
}

func TestAddRoleBindingKeepsExistingGrant(t *testing.T) {
	svc, written := fakeBucketIAM(t, &storage.Policy{
		Bindings: []*storage.PolicyBindings{{Role: testRole, Members: []string{testMember}}},
	})

	if err := AddRoleBindingToGCSBucket(context.Background(), svc, "project", testBucket, testRole, "sql@example.iam.gserviceaccount.com", "instance"); err != nil {
		t.Fatal(err)
	}

	if len(*written) != 0 {
		t.Fatalf("expected no SetIamPolicy call, got %d", len(*written))
	}
}

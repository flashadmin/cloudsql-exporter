package cloudsql

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/api/sqladmin/v1"
	"google.golang.org/api/storage/v1"
)

type InstanceID string
type Databases []string

type Instances map[InstanceID]Databases

// EnumerateCloudSQLDatabaseInstances enumerates Cloud SQL database instances in the given project.
func EnumerateCloudSQLDatabaseInstances(ctx context.Context, sqlAdminSvc *sqladmin.Service, projectID, instanceID string) (Instances, error) {
	log.Printf("Enumerating Cloud SQL instances in project %s", projectID)

	instances := Instances{}

	enumerated := []string{}

	if instanceID == "" {
		instanceList, err := sqlAdminSvc.Instances.List(projectID).Do()
		if err != nil {
			return nil, err
		}
		for _, instance := range instanceList.Items {
			enumerated = append(enumerated, string(instance.Name))
		}
	} else {
		enumerated = append(enumerated, instanceID)
	}

	for _, instance := range enumerated {
		log.Printf("Found instance %s", instance)
		databases, err := ListDatabasesForCloudSQLInstance(ctx, sqlAdminSvc, projectID, instance)
		if err != nil {
			return nil, err
		}
		instances[InstanceID(instance)] = databases
	}

	return instances, nil
}

// GetSvcAcctForCloudSQLInstance returns the service account for the given Cloud SQL sqladmin.Database
func GetSvcAcctForCloudSQLInstance(ctx context.Context, sqlAdminSvc *sqladmin.Service, projectID, instanceID, database string) (string, error) {
	instance, err := sqlAdminSvc.Instances.Get(projectID, instanceID).Do()
	if err != nil {
		return "", err
	}

	return instance.ServiceAccountEmailAddress, nil
}

// AddRoleBindingToGCSBucket adds a role binding to a GCS bucket.
func AddRoleBindingToGCSBucket(ctx context.Context, storageSvc *storage.Service, projectID, bucketName, role, sqlAdminSvcAccount, instance string) error {
	log.Printf("Ensuring role %s to bucket %s for service account %s used by instance %s", role, bucketName, sqlAdminSvcAccount, instance)

	svcAcctMember := fmt.Sprintf("serviceAccount:%s", sqlAdminSvcAccount)

	policy, err := storageSvc.Buckets.GetIamPolicy(bucketName).Do()
	if err != nil {
		return err
	}

	found := false
	for i, binding := range policy.Bindings {
		if binding.Role == role {
			for _, member := range binding.Members {
				if member == svcAcctMember {
					log.Printf("Role %s already exists for service account %s", role, sqlAdminSvcAccount)
					return nil
				}
			}
			policy.Bindings[i].Members = append(policy.Bindings[i].Members, svcAcctMember)
			found = true
			break
		}
	}

	if !found {
		policy.Bindings = append(policy.Bindings, &storage.PolicyBindings{
			Role:    role,
			Members: []string{svcAcctMember},
		})
	}

	_, err = storageSvc.Buckets.SetIamPolicy(bucketName, policy).Do()
	if err != nil {
		return err
	}

	return nil
}

// ListDatabasesForCloudSQLInstance lists the databases for a given Cloud SQL instance.
func ListDatabasesForCloudSQLInstance(ctx context.Context, sqlAdminSvc *sqladmin.Service, projectID, instanceID string) (Databases, error) {
	var databases Databases

	list, err := sqlAdminSvc.Databases.List(projectID, instanceID).Do()
	if err != nil {
		return nil, err
	}

	systemDatabases := map[string]bool{
		// MySQL
		"mysql":              true,
		"information_schema": true,
		"performance_schema": true,
		"sys":                true,
		// PostgreSQL
		"postgres":       true,
		"cloudsqladmin":  true,
		"template0":      true,
		"template1":      true,
	}

	for _, database := range list.Items {
		if systemDatabases[database.Name] {
			log.Printf("Skipping system database %s", database.Name)
			continue
		}
		log.Printf("Found database %s for instance %s", database.Name, instanceID)
		databases = append(databases, database.Name)
	}

	return databases, nil
}

// ExportCloudSQLDatabase exports a Cloud SQL database to a Google Cloud Storage bucket.
func ExportCloudSQLDatabase(ctx context.Context, sqlAdminSvc *sqladmin.Service, databases []string, projectID, instanceID, bucketName, objectName string) error {
	for _, database := range databases {
		log.Printf("Exporting database %s for instance %s", database, instanceID)

		req := &sqladmin.InstancesExportRequest{
			ExportContext: &sqladmin.ExportContext{
				FileType:  "SQL",
				Kind:      "sql#exportContext",
				Databases: []string{database},
				Uri:       fmt.Sprintf("gs://%s/%s/%s/%s/%s", bucketName, projectID, instanceID, database, objectName),
			},
		}

		op, err := sqlAdminSvc.Instances.Export(projectID, instanceID, req).Do()
		if err != nil {
			return err
		}

		err = WaitForSQLOperation(ctx, sqlAdminSvc, projectID, op)
		if err != nil {
			return err
		}
	}

	return nil
}

// operationPollInterval is how often WaitForSQLOperation checks the operation status.
var operationPollInterval = 10 * time.Second

// WaitForSQLOperation polls a Cloud SQL operation until it is DONE. A DONE operation
// can still have failed, so its errors are returned. Only ctx bounds the wait.
func WaitForSQLOperation(ctx context.Context, sqlAdminSvc *sqladmin.Service, gcpProject string, op *sqladmin.Operation) error {
	if op == nil {
		return errors.New("got nil op")
	}

	for {
		if op.Status == "DONE" {
			return operationError(op)
		}

		timer := time.NewTimer(operationPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for operation %s: %w", op.Name, ctx.Err())
		case <-timer.C:
		}

		current, err := sqlAdminSvc.Operations.Get(gcpProject, op.Name).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("waiting for operation %s: %w", op.Name, err)
		}
		op = current
	}
}

// operationError returns the errors of a finished operation, or nil if it succeeded.
func operationError(op *sqladmin.Operation) error {
	if op.Error == nil || len(op.Error.Errors) == 0 {
		return nil
	}

	msgs := make([]string, 0, len(op.Error.Errors))
	for _, e := range op.Error.Errors {
		msgs = append(msgs, fmt.Sprintf("%s: %s", e.Code, e.Message))
	}

	return fmt.Errorf("operation %s failed: %s", op.Name, strings.Join(msgs, "; "))
}

package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

func table(name string) []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: name}}
}

func baseFields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Unique(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func accountFields() []ent.Field {
	return append(baseFields(),
		field.String("owner_user_id").Default(""),
		field.Int64("sub2api_user_id").Default(0),
		field.String("name").Default(""),
		field.String("status").Default("active"),
	)
}

func organizationFields() []ent.Field {
	return append(baseFields(),
		field.String("billing_account_id").Default(""),
		field.String("name").Default(""),
		field.String("status").Default("active"),
	)
}

func userFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").NotEmpty(),
		field.String("email").NotEmpty().Unique(),
		field.String("role").Default("owner"),
		field.String("status").Default("active"),
		field.String("password_hash").Default(""),
		field.String("disabled_at").Default(""),
		field.String("disabled_by").Default(""),
		field.String("disabled_reason").Default(""),
		field.String("deleted_at").Default(""),
		field.String("deleted_by").Default(""),
		field.String("delete_reason").Default(""),
	)
}

func membershipFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").NotEmpty(),
		field.String("organization_id").Default(""),
		field.String("user_id").NotEmpty(),
		field.String("role").Default("member"),
		field.String("status").Default("active"),
	)
}

func sessionFields() []ent.Field {
	return append(baseFields(),
		field.String("user_id").NotEmpty(),
		field.String("csrf").NotEmpty(),
		field.String("expires_at").NotEmpty(),
	)
}

func authAttemptFields() []ent.Field {
	return append(baseFields(),
		field.String("email").Default(""),
		field.String("status").Default(""),
		field.String("reason").Default(""),
		field.String("ip_address").Default(""),
		field.String("user_agent").Default(""),
	)
}

func computeAllocationFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").NotEmpty(),
		field.String("owner_user_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("name").Default(""),
		field.String("package_id").Default(""),
		field.String("provider").Default(""),
		field.String("provider_resource_id").Default(""),
		field.String("provider_request_id").Default(""),
		field.String("operation_id").Default(""),
		field.String("status").Default(""),
		field.String("desired_status").Default(""),
		field.String("provider_status").Default(""),
		field.String("last_provider_sync_at").Default(""),
		field.String("last_provider_sync_error").Default(""),
		field.String("external_deleted_at").Default(""),
		field.String("billing_status").Default(""),
		field.String("pricing_version").Default(""),
		field.String("billing_operation_id").Default(""),
		field.String("billing_state_json").Default("{}"),
		field.String("evidence_id").Default(""),
		field.String("cvm_instance_id").Default(""),
		field.String("instance_id").Default(""),
		field.String("node_name").Default(""),
		field.String("machine_name").Default(""),
		field.Float("cpu").Default(0),
		field.Float("memory_gb").Default(0),
		field.Float("disk_gb").Default(0),
	)
}

func storageVolumeFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").NotEmpty(),
		field.String("owner_user_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("name").Default(""),
		field.String("package_id").Default(""),
		field.String("provider").Default(""),
		field.String("provider_resource_id").Default(""),
		field.String("provider_request_id").Default(""),
		field.String("operation_id").Default(""),
		field.String("status").Default(""),
		field.String("desired_status").Default(""),
		field.String("provider_status").Default(""),
		field.String("last_provider_sync_at").Default(""),
		field.String("last_provider_sync_error").Default(""),
		field.String("external_deleted_at").Default(""),
		field.String("billing_status").Default(""),
		field.String("pricing_version").Default(""),
		field.String("billing_operation_id").Default(""),
		field.String("billing_state_json").Default("{}"),
		field.String("mount_path").Default(""),
		field.Float("size_gb").Default(0),
	)
}

func storageAttachmentFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").NotEmpty(),
		field.String("workspace_id").Default(""),
		field.String("compute_allocation_id").Default(""),
		field.String("storage_id").Default(""),
		field.String("volume_id").Default(""),
		field.String("operation_id").Default(""),
		field.String("provider").Default(""),
		field.String("provider_request_id").Default(""),
		field.String("status").Default(""),
		field.String("mount_path").Default(""),
	)
}

func workspaceFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").Default(""),
		field.String("owner_account_id").Default(""),
		field.String("owner_user_id").Default(""),
		field.String("user_id").Default(""),
		field.String("name").Default(""),
		field.String("url").Default(""),
		field.String("state").Default(""),
		field.String("status").Default(""),
		field.String("billing_state_json").Default("{}"),
		field.String("storage_id").Default(""),
		field.String("current_compute_allocation_id").Default(""),
		field.String("current_attachment_id").Default(""),
		field.String("runtime_id").Default(""),
		field.String("runtime_service_name").Default(""),
		field.String("runtime_service_name_root").Default(""),
		field.String("service_name").Default(""),
		field.String("access_token_status").Default(""),
		field.String("access_account").Default(""),
		field.String("access_username").Default(""),
		field.String("credential_status").Default(""),
		field.String("credential_version").Default(""),
		field.String("credential_secret_ref").Default(""),
		field.Bool("access_requires_login").Default(false),
		field.String("verification_slot_id").Default(""),
		field.Bool("customer_product").Default(true),
	)
}

func workspaceBackupFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").NotEmpty(),
		field.String("workspace_id").NotEmpty(),
		field.String("storage_id").NotEmpty(),
		field.String("snapshot_id").NotEmpty(),
		field.String("status").NotEmpty(),
		field.String("idempotency_key").NotEmpty(),
		field.String("request_hash").NotEmpty(),
		field.String("manifest_json").Default("{}"),
		field.String("restored_storage_id").Default(""),
	)
}

func billingReconciliationFields() []ent.Field {
	return append(baseFields(),
		field.String("status").Default(""),
		field.String("guard_status").Default(""),
		field.String("guard_reason").Default(""),
		field.String("message_author").Default(""),
		field.String("message_text").Default(""),
		field.String("message_created_at").Default(""),
		field.Bool("guard_block_new_workspaces").Default(false),
		field.Int64("reports").Default(0),
	)
}

func runtimeOperationFields() []ent.Field {
	return append(baseFields(),
		field.String("operation_id").Default(""),
		field.String("account_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("resource_id").Default(""),
		field.String("resource_kind").Default(""),
		field.String("action").Default(""),
		field.String("provider").Default(""),
		field.String("provider_request_id").Default(""),
		field.String("status").Default(""),
		field.String("result").Default(""),
		field.String("compute_allocation_id").Default(""),
		field.String("storage_id").Default(""),
		field.String("attachment_id").Default(""),
		field.String("runtime_service_name").Default(""),
		field.String("cvm_instance_id").Default(""),
		field.String("instance_id").Default(""),
		field.String("node_name").Default(""),
		field.String("machine_name").Default(""),
	)
}

func projectTaskSyncHeadFields() []ent.Field {
	return append(baseFields(),
		field.String("kind").NotEmpty(),
		field.String("organization_id").NotEmpty(),
		field.String("workspace_id").NotEmpty(),
		field.String("project_id").Default(""),
		field.String("local_alias_id").Default(""),
		field.Int64("version").Default(1),
		field.String("status").Default("active"),
	)
}

func workspaceSyncEventFields() []ent.Field {
	return append(baseFields(),
		field.String("operation_id").NotEmpty(),
		field.String("workspace_id").NotEmpty(),
		field.Int64("cursor"),
		field.String("entity_kind").NotEmpty(),
		field.String("project_id").NotEmpty(),
		field.String("task_id").Default(""),
		field.String("client_id").NotEmpty(),
		field.String("actor_user_id").NotEmpty(),
		field.Int64("base_version"),
		field.Int64("server_version"),
		field.String("operation").NotEmpty(),
		field.String("status").NotEmpty(),
		field.String("payload_json").Default("{}"),
		field.String("content_digest").Default(""),
		field.String("idempotency_key").NotEmpty().Unique(),
		field.String("request_hash").NotEmpty(),
		field.String("conflict_id").Default(""),
		field.Time("occurred_at"),
	)
}

func executionRequestFields() []ent.Field {
	return append(baseFields(),
		field.String("organization_id").NotEmpty(),
		field.String("workspace_id").NotEmpty(),
		field.String("project_id").NotEmpty(),
		field.String("task_id").NotEmpty(),
		field.String("actor_user_id").NotEmpty(),
		field.String("approval_id").Default(""),
		field.String("approval_status").Default("pending"),
		field.String("approved_by").Default(""),
		field.String("approved_at").Default(""),
		field.String("status").Default("awaiting_approval"),
		field.String("environment_ref").Default(""),
		field.String("job_id").Default(""),
		field.String("receipt_id").Default(""),
		field.String("continuation_id").Default(""),
		field.String("idempotency_key").NotEmpty().Unique(),
		field.Int64("version").Default(1),
	)
}

func adminAuditEventFields() []ent.Field {
	return append(baseFields(),
		field.String("actor_user_id").Default(""),
		field.String("actor_role").Default(""),
		field.String("actor_account_id").Default(""),
		field.String("target_account_id").Default(""),
		field.String("action").Default(""),
		field.String("resource_kind").Default(""),
		field.String("resource_id").Default(""),
		field.String("ip_address").Default(""),
		field.String("user_agent").Default(""),
		field.String("before_json").Default(""),
		field.String("after_json").Default(""),
		field.String("result").Default(""),
	)
}

func supportTicketMappingFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").Default(""),
		field.String("user_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("external_system").Default(""),
		field.String("external_ticket_id").Default(""),
		field.String("external_url").Default(""),
		field.String("operation_id").Default(""),
		field.String("resource_id").Default(""),
		field.String("resource_kind").Default(""),
		field.String("title").Default(""),
		field.String("category").Default(""),
		field.String("priority").Default(""),
		field.String("status").Default(""),
		field.String("source").Default(""),
		field.String("url").Default(""),
		field.String("reason").Default(""),
	)
}

func productionE2ERecordFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("status").Default(""),
		field.String("result").Default(""),
		field.String("reason").Default(""),
		field.String("url").Default(""),
	)
}

func archiveJobFields() []ent.Field {
	return append(baseFields(),
		field.String("resource_kind").Default(""),
		field.String("status").Default(""),
		field.String("reason").Default(""),
		field.Int64("amount_cents").Default(0),
	)
}

func archivedResourceFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").Default(""),
		field.String("workspace_id").Default(""),
		field.String("resource_id").Default(""),
		field.String("resource_kind").Default(""),
		field.String("name").Default(""),
		field.String("status").Default(""),
		field.String("reason").Default(""),
		field.Time("archived_at").Optional().Nillable(),
	)
}

func (Account) Annotations() []schema.Annotation      { return table("control_plane_accounts") }
func (Organization) Annotations() []schema.Annotation { return table("control_plane_organizations") }
func (User) Annotations() []schema.Annotation         { return table("control_plane_users") }
func (Membership) Annotations() []schema.Annotation   { return table("control_plane_memberships") }
func (Session) Annotations() []schema.Annotation      { return table("control_plane_sessions") }
func (AuthAttempt) Annotations() []schema.Annotation  { return table("control_plane_auth_attempts") }
func (ComputeAllocation) Annotations() []schema.Annotation {
	return table("control_plane_compute_allocations")
}
func (StorageVolume) Annotations() []schema.Annotation { return table("control_plane_storage_volumes") }
func (StorageAttachment) Annotations() []schema.Annotation {
	return table("control_plane_storage_attachments")
}
func (Workspace) Annotations() []schema.Annotation { return table("control_plane_workspaces") }
func (BillingReconciliation) Annotations() []schema.Annotation {
	return table("control_plane_billing_reconciliation")
}
func (RuntimeOperation) Annotations() []schema.Annotation {
	return table("control_plane_runtime_operations")
}
func (ProjectTaskSyncHead) Annotations() []schema.Annotation {
	return table("control_plane_project_task_sync_heads")
}
func (WorkspaceSyncEvent) Annotations() []schema.Annotation {
	return table("control_plane_workspace_sync_events")
}
func (ExecutionRequest) Annotations() []schema.Annotation {
	return table("control_plane_execution_requests")
}
func (AdminAuditEvent) Annotations() []schema.Annotation {
	return table("control_plane_admin_audit_events")
}
func (SupportTicketMapping) Annotations() []schema.Annotation {
	return table("control_plane_support_ticket_mappings")
}
func (ProductionE2ERecord) Annotations() []schema.Annotation {
	return table("control_plane_production_e2e_records")
}
func (ArchiveJob) Annotations() []schema.Annotation { return table("control_plane_archive_jobs") }
func (ArchivedComputeAllocation) Annotations() []schema.Annotation {
	return table("control_plane_archived_compute_allocations")
}
func (ArchivedStorageVolume) Annotations() []schema.Annotation {
	return table("control_plane_archived_storage_volumes")
}
func (ArchivedStorageAttachment) Annotations() []schema.Annotation {
	return table("control_plane_archived_storage_attachments")
}
func (ArchivedWorkspace) Annotations() []schema.Annotation {
	return table("control_plane_archived_workspaces")
}
func (ArchivedAdminAuditEvent) Annotations() []schema.Annotation {
	return table("control_plane_archived_admin_audit_events")
}

package schema

import (
	"errors"
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
		field.String("owner_user_id").NotEmpty().Unique(),
		field.Int64("sub2api_user_id").Positive().Unique(),
		field.String("name").Default(""),
		field.String("status").Default("active"),
	)
}

func organizationFields() []ent.Field {
	return append(baseFields(),
		field.String("billing_account_id").NotEmpty().Unique(),
		field.String("name").Default(""),
		field.String("status").Default("active"),
	)
}

func userFields() []ent.Field {
	return append(baseFields(),
		field.String("account_id").NotEmpty().Unique(),
		field.String("email").NotEmpty().Unique(),
		field.String("role").Default("owner"),
		field.String("status").Default("active"),
		field.String("password_hash").Default("").Validate(func(value string) error {
			if value != "" {
				return errors.New("password_hash must be empty")
			}
			return nil
		}),
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
		field.String("account_id").NotEmpty().Unique(),
		field.String("organization_id").NotEmpty().Unique(),
		field.String("user_id").NotEmpty().Unique(),
		field.String("role").Default("owner").Validate(func(value string) error {
			if value != "owner" {
				return errors.New("membership role must be owner")
			}
			return nil
		}),
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
		field.String("purchase_receipt_id").Default(""),
		field.String("billing_state_json").Default("{}"),
		field.String("storage_id").Default(""),
		field.String("current_compute_allocation_id").Default(""),
		field.String("current_attachment_id").Default(""),
		field.String("runtime_id").Default(""),
		field.String("runtime_service_name").Default(""),
		field.String("runtime_service_name_root").Default(""),
		field.String("service_name").Default(""),
		field.Int64("workspace_api_key_id").Optional().Positive(),
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
		field.String("period_start").Default(""),
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

func announcementFields() []ent.Field {
	return append(baseFields(),
		field.String("title").NotEmpty(),
		field.String("body").NotEmpty(),
		field.String("status").Default("draft"),
		field.String("starts_at").Default(""),
		field.String("ends_at").Default(""),
		field.String("published_at").Default(""),
		field.String("created_by_user_id").NotEmpty(),
		field.String("updated_by_user_id").NotEmpty(),
	)
}

func announcementReadFields() []ent.Field {
	return append(baseFields(),
		field.String("announcement_id").NotEmpty(),
		field.String("user_id").NotEmpty(),
		field.String("read_at").NotEmpty(),
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

func remoteSeatCapacityFields() []ent.Field {
	return append(baseFields(),
		field.Int("seat_count").NonNegative(),
		field.Int("seat_limit").Positive().Default(40),
		field.Int("warning_threshold").Positive().Default(35),
	)
}

func remoteInvitationFields() []ent.Field {
	return append(baseFields(),
		field.String("label").Default(""),
		field.String("secret_hash").NotEmpty(),
		field.String("secret_salt").NotEmpty(),
		field.String("expires_at").NotEmpty(),
		field.String("status").Default("active"),
		field.String("consumed_at").Default(""),
		field.String("created_by_user_id").Default(""),
		field.String("idempotency_key").Default(""),
	)
}

func remotePairingFields() []ent.Field {
	return append(baseFields(),
		field.String("invitation_id").NotEmpty(),
		field.String("claim_secret_hash").NotEmpty(),
		field.String("claim_secret_salt").NotEmpty(),
		field.String("manual_code_hash").NotEmpty(),
		field.String("manual_code_salt").NotEmpty(),
		field.Int("manual_attempts").NonNegative(),
		field.String("state").Default("reserved"),
		field.String("expires_at").NotEmpty(),
		field.String("reservation_expires_at").NotEmpty(),
		field.String("desktop_device_id").Default(""),
		field.String("desktop_device_label").Default(""),
		field.String("ios_device_id").Default(""),
		field.String("ios_device_label").Default(""),
		field.String("desktop_public_key").NotEmpty(),
		field.String("ios_public_key").Default(""),
		field.String("sas").Default(""),
		field.String("desktop_provider_user_id").Default(""),
		field.String("ios_provider_user_id").Default(""),
		field.Bool("desktop_provider_absent").Default(false),
		field.Bool("ios_provider_absent").Default(false),
		field.String("confirmed_at").Default(""),
		field.String("revoked_at").Default(""),
		field.Bool("seat_released").Default(false),
		field.String("create_idempotency_key").Default(""),
		field.String("create_request_hash").Default(""),
		field.String("claim_idempotency_key").Default(""),
		field.String("claim_request_hash").Default(""),
		field.String("revocation_receipt_id").Default(""),
		field.String("revocation_receipt_hash").Default(""),
		field.String("revocation_receipt_salt").Default(""),
	)
}

func remoteDeviceCredentialFields() []ent.Field {
	return append(baseFields(),
		field.String("pairing_id").NotEmpty(),
		field.String("device_id").NotEmpty(),
		field.String("role").NotEmpty(),
		field.String("credential_hash").NotEmpty(),
		field.String("status").Default("active"),
		field.String("provider_user_id").Default(""),
		field.String("issued_at").NotEmpty(),
		field.String("revoked_at").Default(""),
		field.String("issued_idempotency_key").Default(""),
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
func (AdminAuditEvent) Annotations() []schema.Annotation {
	return table("control_plane_admin_audit_events")
}
func (Announcement) Annotations() []schema.Annotation {
	return table("control_plane_announcements")
}
func (AnnouncementRead) Annotations() []schema.Annotation {
	return table("control_plane_announcement_reads")
}
func (SupportTicketMapping) Annotations() []schema.Annotation {
	return table("control_plane_support_ticket_mappings")
}
func (ProductionE2ERecord) Annotations() []schema.Annotation {
	return table("control_plane_production_e2e_records")
}
func (ArchivedAdminAuditEvent) Annotations() []schema.Annotation {
	return table("control_plane_archived_admin_audit_events")
}
func (RemoteSeatCapacity) Annotations() []schema.Annotation {
	return table("control_plane_remote_seat_capacities")
}
func (RemoteInvitation) Annotations() []schema.Annotation {
	return table("control_plane_remote_invitations")
}
func (RemotePairing) Annotations() []schema.Annotation {
	return table("control_plane_remote_pairings")
}
func (RemoteDeviceCredential) Annotations() []schema.Annotation {
	return table("control_plane_remote_device_credentials")
}

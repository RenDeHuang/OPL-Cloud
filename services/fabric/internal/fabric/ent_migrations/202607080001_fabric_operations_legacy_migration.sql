DO $$
DECLARE
	table_oid oid;
	id_attnum smallint;
	actual_columns text[];
	current_hard_cut_columns text[] := ARRAY[
		'account_id', 'action', 'caller_service', 'created_at', 'error_code',
		'finished_at', 'id', 'idempotency_key', 'operation_id', 'provider',
		'provider_request_id', 'redacted_provider_payload', 'request_hash',
		'resource_id', 'resource_kind', 'retryable', 'started_at', 'status',
		'workspace_id'
	];
	current_product_columns text[] := ARRAY[
		'account_id', 'action', 'caller_service', 'compute_pool_key',
		'compute_pool_lease_expires_at', 'compute_pool_lease_owner', 'created_at',
		'error_code', 'finished_at', 'id', 'idempotency_key', 'operation_id',
		'provider', 'provider_request_id', 'redacted_provider_payload',
		'request_hash', 'resource_id', 'resource_kind', 'retryable', 'started_at',
		'status', 'workspace_id'
	];
	transformed_hybrid_columns text[] := ARRAY[
		'account_id', 'action', 'attempts', 'caller_service', 'correlation_id',
		'created_at', 'error_code', 'evidence_refs', 'finished_at', 'id',
		'idempotency_key', 'last_error', 'lease_expires_at', 'lease_owner',
		'operation_id', 'provider', 'provider_refs', 'provider_request_id',
		'redacted_provider_payload', 'request_hash', 'requested_by', 'resource_id',
		'resource_kind', 'retryable', 'started_at', 'state', 'status', 'updated_at',
		'workspace_id'
	];
	expected_columns text[] := ARRAY[
		'attempts', 'correlation_id', 'created_at', 'evidence_refs', 'id',
		'idempotency_key', 'last_error', 'lease_expires_at', 'lease_owner',
		'provider_refs', 'requested_by', 'resource_id', 'resource_kind',
		'state', 'updated_at'
	];
	non_primary_indexes integer;
	idempotency_index_name text;
	idempotency_constraint_name text;
	inbound_fk_count integer;
	inbound_fk_sources text[];
BEGIN
	table_oid := to_regclass('fabric_operations');
	IF table_oid IS NULL THEN
		RETURN;
	END IF;
	IF NOT EXISTS (
		SELECT 1
		FROM pg_class
		WHERE oid = table_oid AND relkind = 'r'
	) THEN
		RAISE EXCEPTION 'fabric_operations is not a table';
	END IF;

	SELECT array_agg(attname ORDER BY attname)
	INTO actual_columns
	FROM pg_attribute
	WHERE attrelid = table_oid
	  AND attnum > 0
	  AND NOT attisdropped;
	SELECT attnum
	INTO id_attnum
	FROM pg_attribute
	WHERE attrelid = table_oid AND attname = 'id' AND NOT attisdropped;
	IF EXISTS (
		SELECT 1
		FROM opl_schema_migrations
		WHERE service = 'fabric'
		  AND version = '202607090001_ent_hard_cut'
	) THEN
		IF actual_columns IS DISTINCT FROM current_hard_cut_columns
		   AND actual_columns IS DISTINCT FROM current_product_columns THEN
			RAISE EXCEPTION 'journaled fabric_operations has an unknown current shape';
		END IF;
		RETURN;
	END IF;
	IF actual_columns = transformed_hybrid_columns THEN
		IF EXISTS (
			SELECT 1
			FROM pg_attribute a
			WHERE a.attrelid = table_oid
			  AND a.attnum > 0
			  AND NOT a.attisdropped
			  AND (
				CASE a.attname
					WHEN 'id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
					WHEN 'correlation_id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'idempotency_key' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
					WHEN 'requested_by' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'resource_id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
					WHEN 'resource_kind' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
					WHEN 'state' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'lease_owner' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'lease_expires_at' THEN format_type(a.atttypid, a.atttypmod) = 'timestamp with time zone' AND NOT a.attnotnull AND NOT a.atthasdef
					WHEN 'attempts' THEN format_type(a.atttypid, a.atttypmod) = 'integer' AND a.attnotnull AND a.atthasdef
					WHEN 'last_error' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'provider_refs' THEN format_type(a.atttypid, a.atttypmod) = 'jsonb' AND a.attnotnull AND a.atthasdef
					WHEN 'evidence_refs' THEN format_type(a.atttypid, a.atttypmod) = 'jsonb' AND a.attnotnull AND a.atthasdef
					WHEN 'created_at' THEN format_type(a.atttypid, a.atttypmod) = 'timestamp with time zone' AND a.attnotnull AND a.atthasdef
					WHEN 'updated_at' THEN format_type(a.atttypid, a.atttypmod) = 'timestamp with time zone' AND a.attnotnull AND a.atthasdef
					WHEN 'operation_id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
					WHEN 'caller_service' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
					WHEN 'action' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'account_id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'workspace_id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'provider' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'provider_request_id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'request_hash' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'redacted_provider_payload' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'status' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
					WHEN 'error_code' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
					WHEN 'retryable' THEN format_type(a.atttypid, a.atttypmod) = 'boolean' AND a.attnotnull AND a.atthasdef
					WHEN 'started_at' THEN format_type(a.atttypid, a.atttypmod) = 'timestamp with time zone' AND a.attnotnull AND NOT a.atthasdef
					WHEN 'finished_at' THEN format_type(a.atttypid, a.atttypmod) = 'timestamp with time zone' AND NOT a.attnotnull AND NOT a.atthasdef
					ELSE false
				END = false
			  )
		) THEN
			RAISE EXCEPTION 'fabric_operations has an unknown transformed hybrid definition';
		END IF;
		IF EXISTS (
			SELECT 1
			FROM pg_attribute a
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE a.attrelid = table_oid
			  AND a.attnum > 0
			  AND NOT a.attisdropped
			  AND (
				(a.attname IN ('correlation_id', 'requested_by', 'state', 'lease_owner', 'attempts', 'last_error', 'provider_refs', 'evidence_refs', 'created_at', 'updated_at', 'action', 'account_id', 'workspace_id', 'provider', 'provider_request_id', 'request_hash', 'redacted_provider_payload', 'error_code', 'retryable')
				 AND (d.oid IS NULL OR regexp_replace(lower(trim(pg_get_expr(d.adbin, d.adrelid))), '[[:space:]]+', '', 'g') <>
					CASE a.attname
						WHEN 'correlation_id' THEN $default$''::text$default$
						WHEN 'requested_by' THEN $default$''::text$default$
						WHEN 'state' THEN $default$''::text$default$
						WHEN 'lease_owner' THEN $default$''::text$default$
						WHEN 'attempts' THEN '0'
						WHEN 'last_error' THEN $default$''::text$default$
						WHEN 'provider_refs' THEN $default$'{}'::jsonb$default$
						WHEN 'evidence_refs' THEN $default$'[]'::jsonb$default$
						WHEN 'created_at' THEN 'now()'
						WHEN 'updated_at' THEN 'now()'
						WHEN 'action' THEN $default$''::text$default$
						WHEN 'account_id' THEN $default$''::text$default$
						WHEN 'workspace_id' THEN $default$''::text$default$
						WHEN 'provider' THEN $default$''::text$default$
						WHEN 'provider_request_id' THEN $default$''::text$default$
						WHEN 'request_hash' THEN $default$''::text$default$
						WHEN 'redacted_provider_payload' THEN $default$'{}'::text$default$
						WHEN 'error_code' THEN $default$''::text$default$
						WHEN 'retryable' THEN 'false'
					END))
			  )
		) THEN
			RAISE EXCEPTION 'fabric_operations has an unknown transformed hybrid default';
		END IF;
		IF EXISTS (
			SELECT 1 FROM pg_index
			WHERE indrelid = table_oid AND NOT indisprimary
		) THEN
			RAISE EXCEPTION 'fabric_operations has unexpected transformed hybrid indexes';
		END IF;
		IF (
			SELECT count(*)
			FROM pg_constraint
			WHERE conrelid = table_oid AND contype = 'p'
		) <> 1 OR (
			SELECT count(*)
			FROM pg_constraint
			WHERE conrelid = table_oid
			  AND contype = 'p'
			  AND conkey = ARRAY[id_attnum]::smallint[]
		) <> 1 OR EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid = table_oid AND contype <> 'p'
		) THEN
			RAISE EXCEPTION 'fabric_operations has unexpected transformed hybrid constraints';
		END IF;
		SELECT count(*)
		INTO inbound_fk_count
		FROM pg_constraint
		WHERE contype = 'f' AND confrelid = table_oid;
		IF inbound_fk_count <> 4 THEN
			RAISE EXCEPTION 'fabric_operations has unexpected transformed hybrid foreign-key count';
		END IF;
		SELECT array_agg(source.relname ORDER BY source.relname)
		INTO inbound_fk_sources
		FROM pg_constraint c
		JOIN pg_class source ON source.oid = c.conrelid
		JOIN pg_attribute local_column
		  ON local_column.attrelid = c.conrelid
		 AND local_column.attnum = c.conkey[1]
		WHERE c.contype = 'f'
		  AND c.confrelid = table_oid
		  AND array_length(c.conkey, 1) = 1
		  AND local_column.attname = 'operation_id';
		IF inbound_fk_sources IS DISTINCT FROM ARRAY[
			'fabric_events', 'fabric_evidence_refs', 'idempotency_keys', 'workspaces'
		]::text[] THEN
			RAISE EXCEPTION 'fabric_operations has unexpected transformed hybrid foreign-key sources';
		END IF;
		IF EXISTS (
			SELECT 1
			FROM pg_constraint c
			JOIN pg_attribute local_column ON local_column.attrelid = c.conrelid AND local_column.attnum = c.conkey[1]
			WHERE c.contype = 'f'
			  AND c.confrelid = table_oid
			  AND (
				array_length(c.conkey, 1) <> 1 OR array_length(c.confkey, 1) <> 1
				OR local_column.attname <> 'operation_id'
				OR c.confkey[1] <> id_attnum
				OR c.confmatchtype <> 's' OR c.confupdtype <> 'a' OR c.confdeltype <> 'a'
				OR c.condeferrable OR c.condeferred OR NOT c.convalidated
			  )
		) THEN
			RAISE EXCEPTION 'fabric_operations has an unknown transformed hybrid foreign-key shape';
		END IF;
		IF EXISTS (
			SELECT correlation_id
			FROM fabric_operations
			GROUP BY correlation_id
			HAVING count(*) > 1
		) THEN
			RAISE EXCEPTION 'fabric_operations has ambiguous transformed hybrid correlation_id identity';
		END IF;
		IF EXISTS (
			SELECT 1
			FROM fabric_operations
			WHERE operation_id IS DISTINCT FROM correlation_id
			   OR caller_service IS DISTINCT FROM requested_by
			   OR status IS DISTINCT FROM state
			   OR started_at IS DISTINCT FROM created_at
		) THEN
			RAISE EXCEPTION 'fabric_operations has an unknown transformed hybrid row mapping';
		END IF;
		RETURN;
	END IF;
	IF actual_columns IS DISTINCT FROM expected_columns THEN
		RAISE EXCEPTION 'fabric_operations has an unknown historical shape';
	END IF;
	IF EXISTS (
		SELECT correlation_id
		FROM fabric_operations
		GROUP BY correlation_id
		HAVING count(*) > 1
	) THEN
		RAISE EXCEPTION 'fabric_operations has ambiguous correlation_id identity';
	END IF;

	IF EXISTS (
		SELECT 1
		FROM pg_attribute a
		WHERE a.attrelid = table_oid
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		  AND (
			CASE a.attname
				WHEN 'id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
				WHEN 'correlation_id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
				WHEN 'idempotency_key' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
				WHEN 'requested_by' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
				WHEN 'resource_id' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
				WHEN 'resource_kind' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
				WHEN 'state' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND NOT a.atthasdef
					WHEN 'lease_owner' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
				WHEN 'lease_expires_at' THEN format_type(a.atttypid, a.atttypmod) = 'timestamp with time zone' AND NOT a.attnotnull AND NOT a.atthasdef
				WHEN 'attempts' THEN format_type(a.atttypid, a.atttypmod) = 'integer' AND a.attnotnull AND a.atthasdef
				WHEN 'last_error' THEN format_type(a.atttypid, a.atttypmod) = 'text' AND a.attnotnull AND a.atthasdef
				WHEN 'provider_refs' THEN format_type(a.atttypid, a.atttypmod) = 'jsonb' AND a.attnotnull AND a.atthasdef
				WHEN 'evidence_refs' THEN format_type(a.atttypid, a.atttypmod) = 'jsonb' AND a.attnotnull AND a.atthasdef
				WHEN 'created_at' THEN format_type(a.atttypid, a.atttypmod) = 'timestamp with time zone' AND a.attnotnull AND a.atthasdef
				WHEN 'updated_at' THEN format_type(a.atttypid, a.atttypmod) = 'timestamp with time zone' AND a.attnotnull AND a.atthasdef
				ELSE false
			END = false
		  )
	) THEN
		RAISE EXCEPTION 'fabric_operations has unexpected historical column definition';
	END IF;

	IF EXISTS (
		SELECT 1
		FROM pg_attribute a
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = table_oid
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		  AND (
			(a.attname IN ('id', 'correlation_id', 'idempotency_key', 'requested_by', 'resource_id', 'resource_kind', 'state', 'lease_expires_at')
			 AND d.oid IS NOT NULL)
			OR (a.attname IN ('lease_owner', 'attempts', 'last_error', 'provider_refs', 'evidence_refs', 'created_at', 'updated_at')
				AND (d.oid IS NULL
				OR regexp_replace(lower(trim(pg_get_expr(d.adbin, d.adrelid))), '[[:space:]]+', '', 'g') <>
					CASE a.attname
						WHEN 'lease_owner' THEN $default$''::text$default$
						WHEN 'attempts' THEN '0'
						WHEN 'last_error' THEN $default$''::text$default$
						WHEN 'provider_refs' THEN $default$'{}'::jsonb$default$
						WHEN 'evidence_refs' THEN $default$'[]'::jsonb$default$
						WHEN 'created_at' THEN 'now()'
						WHEN 'updated_at' THEN 'now()'
					END))
		  )
	) THEN
		RAISE EXCEPTION 'fabric_operations has unexpected historical default';
	END IF;

	IF (
		SELECT count(*)
		FROM pg_constraint
		WHERE conrelid = table_oid AND contype = 'p'
	) <> 1 OR (
		SELECT count(*)
		FROM pg_constraint c
		WHERE c.conrelid = table_oid
		  AND c.contype = 'p'
		  AND c.conkey = ARRAY[id_attnum]::smallint[]
	) <> 1 THEN
		RAISE EXCEPTION 'fabric_operations has an unexpected primary key';
	END IF;

	IF EXISTS (
		SELECT 1
		FROM pg_constraint
		WHERE conrelid = table_oid
		  AND contype NOT IN ('p', 'u')
	) OR (
		SELECT count(*)
		FROM pg_constraint
		WHERE conrelid = table_oid
		  AND contype = 'u'
	) > 1 THEN
		RAISE EXCEPTION 'fabric_operations has unexpected table constraints';
	END IF;

	SELECT count(*)
	INTO non_primary_indexes
	FROM pg_index
	WHERE indrelid = table_oid
	  AND NOT indisprimary;
	IF non_primary_indexes > 1 THEN
		RAISE EXCEPTION 'fabric_operations has unexpected indexes';
	END IF;
	IF non_primary_indexes = 1 THEN
		SELECT c.relname
		INTO idempotency_index_name
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		WHERE i.indrelid = table_oid
		  AND NOT i.indisprimary
		  AND i.indisunique
		  AND i.indnkeyatts = 1
		  AND i.indnatts = 1
		  AND i.indpred IS NULL
		  AND i.indexprs IS NULL
		  AND i.indkey[0] = (
			SELECT attnum FROM pg_attribute
			WHERE attrelid = table_oid AND attname = 'idempotency_key' AND NOT attisdropped
		  );
		IF idempotency_index_name IS NULL THEN
			RAISE EXCEPTION 'fabric_operations has an unexpected non-primary index';
		END IF;
		SELECT con.conname
		INTO idempotency_constraint_name
		FROM pg_constraint con
		WHERE con.conrelid = table_oid
		  AND con.contype = 'u'
		  AND con.conindid = (
			SELECT oid FROM pg_class WHERE relname = idempotency_index_name
		  )
		  AND con.conkey = ARRAY[(
			SELECT attnum FROM pg_attribute
			WHERE attrelid = table_oid AND attname = 'idempotency_key' AND NOT attisdropped
		  )]::smallint[]
		  AND NOT con.condeferrable
		  AND NOT con.condeferred
		  AND con.convalidated;
		IF idempotency_constraint_name IS NULL THEN
			RAISE EXCEPTION 'fabric_operations idempotency uniqueness is not the admitted constraint';
		END IF;
	END IF;

	SELECT count(*)
	INTO inbound_fk_count
	FROM pg_constraint
	WHERE contype = 'f'
	  AND confrelid = table_oid;
	IF inbound_fk_count <> 4 THEN
		RAISE EXCEPTION 'fabric_operations has an unexpected inbound foreign-key count';
	END IF;
	SELECT array_agg(source.relname ORDER BY source.relname)
	INTO inbound_fk_sources
	FROM pg_constraint c
	JOIN pg_class source ON source.oid = c.conrelid
	JOIN pg_attribute local_column
	  ON local_column.attrelid = c.conrelid
	 AND local_column.attnum = c.conkey[1]
	WHERE c.contype = 'f'
	  AND c.confrelid = table_oid
	  AND array_length(c.conkey, 1) = 1
	  AND local_column.attname = 'operation_id';
	IF inbound_fk_sources IS DISTINCT FROM ARRAY[
		'fabric_events', 'fabric_evidence_refs', 'idempotency_keys', 'workspaces'
	]::text[] THEN
		RAISE EXCEPTION 'fabric_operations has unexpected inbound foreign-key sources';
	END IF;
	IF EXISTS (
		SELECT 1
		FROM pg_constraint c
		WHERE c.contype = 'f'
		  AND c.confrelid = table_oid
		  AND (
			array_length(c.conkey, 1) <> 1
			OR array_length(c.confkey, 1) <> 1
			OR c.confkey[1] <> id_attnum
			OR c.confmatchtype <> 's'
			OR c.confupdtype <> 'a'
			OR c.confdeltype <> 'a'
			OR c.condeferrable
			OR c.condeferred
			OR NOT c.convalidated
		  )
	) THEN
		RAISE EXCEPTION 'fabric_operations has an unexpected inbound foreign-key shape';
	END IF;

	IF idempotency_index_name IS NOT NULL THEN
		EXECUTE format('ALTER TABLE fabric_operations DROP CONSTRAINT %I', idempotency_constraint_name);
	END IF;

	ALTER TABLE fabric_operations
		ADD COLUMN operation_id TEXT,
		ADD COLUMN caller_service TEXT,
		ADD COLUMN action TEXT NOT NULL DEFAULT '',
		ADD COLUMN account_id TEXT NOT NULL DEFAULT '',
		ADD COLUMN workspace_id TEXT NOT NULL DEFAULT '',
		ADD COLUMN provider TEXT NOT NULL DEFAULT '',
		ADD COLUMN provider_request_id TEXT NOT NULL DEFAULT '',
		ADD COLUMN request_hash TEXT NOT NULL DEFAULT '',
		ADD COLUMN redacted_provider_payload TEXT NOT NULL DEFAULT '{}',
		ADD COLUMN status TEXT,
		ADD COLUMN error_code TEXT NOT NULL DEFAULT '',
		ADD COLUMN retryable BOOLEAN NOT NULL DEFAULT false,
		ADD COLUMN started_at TIMESTAMPTZ,
		ADD COLUMN finished_at TIMESTAMPTZ;

	-- Historical correlation_id is the preserved deterministic operation identity.
	UPDATE fabric_operations
	SET operation_id = correlation_id,
		caller_service = requested_by,
		status = state,
		started_at = created_at;

	ALTER TABLE fabric_operations
		ALTER COLUMN operation_id SET NOT NULL,
		ALTER COLUMN caller_service SET NOT NULL,
		ALTER COLUMN status SET NOT NULL,
		ALTER COLUMN started_at SET NOT NULL,
		ALTER COLUMN correlation_id SET DEFAULT '',
		ALTER COLUMN requested_by SET DEFAULT '',
		ALTER COLUMN state SET DEFAULT '';
END
$$;

-- Synthetic schema 77, generated from 428e44bb3d6b044b7fd30c362457dd471ff501b9.
CREATE TABLE account_balance_adjustments (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			account_id TEXT NOT NULL,
			created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
			created_at INTEGER NOT NULL,
			expected_version INTEGER NOT NULL CHECK (typeof(expected_version) = 'integer' AND expected_version >= 0),
			before_balance INTEGER NOT NULL CHECK (typeof(before_balance) = 'integer'),
			after_balance INTEGER NOT NULL CHECK (typeof(after_balance) = 'integer'),
			delta_cents INTEGER NOT NULL CHECK (typeof(delta_cents) = 'integer' AND delta_cents != 0),
			reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 1 AND 500),
			UNIQUE (workspace_id, account_id, expected_version),
			FOREIGN KEY (account_id, workspace_id) REFERENCES accounts(id, workspace_id) ON DELETE CASCADE,
			CHECK (typeof(after_balance - before_balance) = 'integer' AND delta_cents = after_balance - before_balance)
		);
CREATE TABLE "accounts" (
						id              TEXT PRIMARY KEY,
						workspace_id    TEXT NOT NULL,
						name            TEXT NOT NULL,
					type            TEXT NOT NULL CHECK (type IN ('CHECKING', 'SAVINGS', 'INVESTMENT', 'WALLET', 'CREDIT_CARD')),
					color           TEXT NOT NULL DEFAULT '#6B7280',
					icon            TEXT NOT NULL DEFAULT '',
					provider_slug   TEXT NOT NULL DEFAULT 'custom',
					initial_balance INTEGER NOT NULL DEFAULT 0,
						current_balance INTEGER NOT NULL DEFAULT 0,
						created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
						updated_at      INTEGER NOT NULL DEFAULT (unixepoch()), archived_at INTEGER NULL, sort_order INTEGER NOT NULL DEFAULT 0, balance_version INTEGER NOT NULL DEFAULT 0 CHECK (typeof(balance_version) = 'integer' AND balance_version >= 0),
						FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
					);
CREATE TABLE auth_audit_events (
    id            TEXT PRIMARY KEY,
    user_id       TEXT,
    workspace_id  TEXT,
    event_type    TEXT NOT NULL,
    ip            TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL
);
CREATE TABLE auth_lockouts (
    user_id               TEXT PRIMARY KEY,
    failed_password_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_password_count >= 0),
    failed_2fa_count      INTEGER NOT NULL DEFAULT 0 CHECK (failed_2fa_count >= 0),
    first_failed_at       INTEGER NOT NULL DEFAULT 0,
    last_failed_at        INTEGER NOT NULL DEFAULT 0,
    locked_until          INTEGER NOT NULL DEFAULT 0,
    lock_reason           TEXT NOT NULL DEFAULT '' CHECK (lock_reason IN ('', 'password', 'totp')),
    updated_at            INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE box_contribution_lifecycle_events (
	id             TEXT PRIMARY KEY,
	box_id         TEXT    NOT NULL,
	event_type     TEXT    NOT NULL CHECK (event_type IN ('GOAL_REACHED', 'CONTRIBUTION_BLOCKED', 'CONTRIBUTION_REACTIVATED')),
	previous_state TEXT CHECK (previous_state IS NULL OR previous_state IN ('ACTIVE', 'BLOCKED')),
	new_state      TEXT CHECK (new_state IS NULL OR new_state IN ('ACTIVE', 'BLOCKED')),
	occurred_at    INTEGER NOT NULL,
	CHECK (
		(event_type = 'GOAL_REACHED' AND previous_state IS NULL AND new_state IS NULL) OR
		(event_type = 'CONTRIBUTION_BLOCKED' AND previous_state = 'ACTIVE' AND new_state = 'BLOCKED') OR
		(event_type = 'CONTRIBUTION_REACTIVATED' AND previous_state = 'BLOCKED' AND new_state = 'ACTIVE')
	),
	FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE
);
CREATE TABLE "box_virtual_ledger" (
				id                    TEXT PRIMARY KEY,
				box_id                TEXT    NOT NULL,
				amount                INTEGER NOT NULL CHECK (
					(type IN ('RECHARGE', 'BONUS', 'REVERSAL') AND amount > 0) OR
					(type IN ('RELEASE', 'CONSUME') AND amount < 0)
				),
				type                  TEXT    NOT NULL CHECK (type IN ('RECHARGE', 'BONUS', 'RELEASE', 'CONSUME', 'REVERSAL')),
				description           TEXT    NOT NULL DEFAULT '',
				source_transaction_id TEXT,
				reversal_of_ledger_id TEXT,
				reference_date        INTEGER NOT NULL,
				created_at            INTEGER NOT NULL DEFAULT (unixepoch()), automatic_recharge_month TEXT CHECK (
		automatic_recharge_month IS NULL OR (
			type = 'RECHARGE' AND
			length(automatic_recharge_month) = 7 AND
			substr(automatic_recharge_month, 5, 1) = '-' AND
			substr(automatic_recharge_month, 1, 4) NOT GLOB '*[^0-9]*' AND
			substr(automatic_recharge_month, 6, 2) NOT GLOB '*[^0-9]*' AND
			CAST(substr(automatic_recharge_month, 6, 2) AS INTEGER) BETWEEN 1 AND 12
		)
	),
				FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
				FOREIGN KEY (reversal_of_ledger_id) REFERENCES "box_virtual_ledger"(id) ON DELETE SET NULL
			);
CREATE TABLE boxes (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT    NOT NULL,
    name             TEXT    NOT NULL,
    description      TEXT    NOT NULL DEFAULT '',
    category_id      TEXT,
    target_amount    INTEGER NOT NULL DEFAULT 0,
    monthly_recharge INTEGER NOT NULL DEFAULT 0,
	continue_after_target INTEGER NOT NULL DEFAULT 1 CHECK (continue_after_target IN (0, 1)),
	automatic_contribution_state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (automatic_contribution_state IN ('ACTIVE', 'BLOCKED')),
    target_date      INTEGER,
    monthly_yield_rate REAL  NOT NULL DEFAULT 0.0,
	monthly_recharge_anchor_month TEXT CHECK (
		monthly_recharge_anchor_month IS NULL OR (
			length(monthly_recharge_anchor_month) = 7 AND
			substr(monthly_recharge_anchor_month, 5, 1) = '-' AND
			substr(monthly_recharge_anchor_month, 1, 4) NOT GLOB '*[^0-9]*' AND
			substr(monthly_recharge_anchor_month, 6, 2) NOT GLOB '*[^0-9]*' AND
			CAST(substr(monthly_recharge_anchor_month, 6, 2) AS INTEGER) BETWEEN 1 AND 12
		)
	),
    created_at       INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at       INTEGER NOT NULL DEFAULT (unixepoch()), icon_key TEXT,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (category_id)  REFERENCES categories(id) ON DELETE SET NULL
);
CREATE TABLE "categories" (
				id           TEXT PRIMARY KEY,
				workspace_id TEXT NOT NULL,
				name         TEXT NOT NULL,
				icon         TEXT NOT NULL DEFAULT 'tag',
				color        TEXT NOT NULL DEFAULT '#6b7280',
				type         TEXT NOT NULL CHECK (type IN ('EXPENSE', 'INCOME')),
				macro_group  TEXT,
				parent_id    TEXT,
				is_fixed     INTEGER NOT NULL DEFAULT 0 CHECK (is_fixed IN (0, 1)),
				created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
				FOREIGN KEY (parent_id) REFERENCES "categories"(id) ON DELETE CASCADE
			);
CREATE TABLE contacts (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    custom_client_id TEXT,
    name         TEXT NOT NULL,
    document     TEXT NOT NULL DEFAULT '',
    type         TEXT NOT NULL CHECK (type IN ('client', 'vendor')),
    email        TEXT NOT NULL DEFAULT '',
    phone        TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    UNIQUE (workspace_id, custom_client_id)
);
CREATE TABLE cost_limits (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT    NOT NULL,
    category_id        TEXT    NOT NULL,
    max_amount_monthly INTEGER NOT NULL CHECK (max_amount_monthly > 0),
    alert_threshold    INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT (unixepoch()), updated_at INTEGER NOT NULL DEFAULT (unixepoch()), icon_key TEXT,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (category_id)  REFERENCES categories(id) ON DELETE CASCADE,
    UNIQUE (workspace_id, category_id)
);
CREATE TABLE credit_cards (
    id                          TEXT PRIMARY KEY,
    account_id                  TEXT    NOT NULL UNIQUE,
    closing_day                 INTEGER NOT NULL CHECK (closing_day BETWEEN 1 AND 31),
    due_day                     INTEGER NOT NULL CHECK (due_day BETWEEN 1 AND 31),
    credit_limit                INTEGER NOT NULL DEFAULT 0,
    default_payment_account_id  TEXT,
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
);
CREATE TABLE investment_adoption_attempts (
 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL, id TEXT NOT NULL, request_id TEXT NOT NULL CHECK(length(request_id)=36),
 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
 code TEXT NOT NULL CHECK(code IN ('CONFLICT','VALIDATION','NOT_FOUND','INVALID_STATUS','ADMISSION_PENDING')),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)), conflicting_confirmation_id TEXT,
 PRIMARY KEY(workspace_id,account_id,id),
 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,account_id,conflicting_confirmation_id) REFERENCES investment_adoption_confirmations(workspace_id,account_id,id) ON DELETE RESTRICT
 );
CREATE TABLE investment_adoption_confirmations (
 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL, id TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version>0),
 previous_id TEXT, previous_version INTEGER NOT NULL CHECK(previous_version=version-1),
 cut_date TEXT NOT NULL CHECK(COALESCE(typeof(cut_date)='text' AND length(CAST(cut_date AS BLOB))=10 AND
	 cut_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
	 CAST(substr(cut_date,1,4) AS INTEGER) BETWEEN 1 AND 9999 AND
	 CAST(substr(cut_date,6,2) AS INTEGER) BETWEEN 1 AND 12 AND
	 CAST(substr(cut_date,9,2) AS INTEGER) BETWEEN 1 AND CASE CAST(substr(cut_date,6,2) AS INTEGER)
	 WHEN 2 THEN 28+(CAST(substr(cut_date,1,4) AS INTEGER)%4=0 AND (CAST(substr(cut_date,1,4) AS INTEGER)%100!=0 OR CAST(substr(cut_date,1,4) AS INTEGER)%400=0))
	 WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30 ELSE 31 END,0)=1),
 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
 request_id TEXT NOT NULL CHECK(length(request_id)=36), reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
 evidence_version INTEGER NOT NULL CHECK(evidence_version=1), declaration_version INTEGER NOT NULL CHECK(declaration_version=1),
 evidence_token TEXT NOT NULL CHECK(length(evidence_token)=64), source_token TEXT NOT NULL CHECK(length(source_token)=64),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)), evidence_json TEXT NOT NULL CHECK(json_valid(evidence_json)),
 result_json TEXT NOT NULL CHECK(CASE WHEN json_valid(result_json) THEN COALESCE((json_type(result_json)='object' AND json_type(result_json,'$.ID')='text' AND json_extract(result_json,'$.ID')=id AND json_type(result_json,'$.WorkspaceID')='text' AND json_extract(result_json,'$.WorkspaceID')=workspace_id AND json_type(result_json,'$.AccountID')='text' AND json_extract(result_json,'$.AccountID')=account_id AND json_type(result_json,'$.Version')='integer' AND json_extract(result_json,'$.Version')=version AND json_type(result_json,'$.CutDate')='text' AND json_extract(result_json,'$.CutDate')=cut_date AND json_type(result_json,'$.CreatedBy')='text' AND json_extract(result_json,'$.CreatedBy')=created_by AND json_type(result_json,'$.CreatedAt')='integer' AND json_extract(result_json,'$.CreatedAt')=created_at AND json_type(result_json,'$.RequestID')='text' AND json_extract(result_json,'$.RequestID')=request_id AND json_type(result_json,'$.Reason')='text' AND json_extract(result_json,'$.Reason')=reason AND json_type(result_json,'$.IntegrationAdmission')='text' AND json_extract(result_json,'$.IntegrationAdmission')=integration_admission AND json_type(result_json,'$.FinancialEffectCents')='integer' AND json_extract(result_json,'$.FinancialEffectCents')=financial_effect_cents AND json_type(result_json,'$.Declaration.WorkspaceID')='text' AND json_extract(result_json,'$.Declaration.WorkspaceID')=workspace_id AND json_type(result_json,'$.Declaration.AccountID')='text' AND json_extract(result_json,'$.Declaration.AccountID')=account_id AND json_type(result_json,'$.Declaration.CutDate')='text' AND json_extract(result_json,'$.Declaration.CutDate')=cut_date AND json_type(result_json,'$.Declaration.CreatedBy')='text' AND json_extract(result_json,'$.Declaration.CreatedBy')=created_by AND json_type(result_json,'$.Declaration.RequestID')='text' AND json_extract(result_json,'$.Declaration.RequestID')=request_id AND json_type(result_json,'$.Declaration.Reason')='text' AND json_extract(result_json,'$.Declaration.Reason')=reason AND json_type(result_json,'$.Declaration.ExpectedAdoptionVersion')='integer' AND json_extract(result_json,'$.Declaration.ExpectedAdoptionVersion')=previous_version AND json_type(result_json,'$.Declaration.EvidenceVersion')='integer' AND json_extract(result_json,'$.Declaration.EvidenceVersion')=evidence_version AND json_type(result_json,'$.Declaration.DeclarationVersion')='integer' AND json_extract(result_json,'$.Declaration.DeclarationVersion')=declaration_version AND json_type(result_json,'$.Declaration.EvidenceToken')='text' AND json_extract(result_json,'$.Declaration.EvidenceToken')=evidence_token AND json_type(result_json,'$.Diagnostic.WorkspaceID')='text' AND json_extract(result_json,'$.Diagnostic.WorkspaceID')=workspace_id AND json_type(result_json,'$.Diagnostic.AccountID')='text' AND json_extract(result_json,'$.Diagnostic.AccountID')=account_id AND json_type(result_json,'$.Diagnostic.CutDate')='text' AND json_extract(result_json,'$.Diagnostic.CutDate')=cut_date AND json_type(result_json,'$.ObservedBalanceCents')='integer' AND json_extract(result_json,'$.ObservedBalanceCents')>=0 AND json_type(result_json,'$.ObservedBalanceVersion')='integer' AND json_extract(result_json,'$.ObservedBalanceVersion')>=0 AND json_type(result_json,'$.Coverage')='text' AND json_extract(result_json,'$.Coverage') IN ('PARTIAL','ALL_IDENTIFIED') AND json_type(result_json,'$.Items')='array' AND json_array_length(result_json,'$.Items')>0 AND json_type(result_json,'$.Declaration')='object' AND json_type(result_json,'$.Diagnostic')='object' AND json_type(result_json,'$.Declaration.ExpectedBalanceVersion')='integer' AND json_extract(result_json,'$.Declaration.ExpectedBalanceVersion')=json_extract(result_json,'$.ObservedBalanceVersion') AND json_type(result_json,'$.Diagnostic.CurrentBalanceCents')='integer' AND json_extract(result_json,'$.Diagnostic.CurrentBalanceCents')=json_extract(result_json,'$.ObservedBalanceCents') AND json_type(result_json,'$.Diagnostic.BalanceVersion')='integer' AND json_extract(result_json,'$.Diagnostic.BalanceVersion')=json_extract(result_json,'$.ObservedBalanceVersion') AND json_type(result_json,'$.Declaration.Coverage')='text' AND json_extract(result_json,'$.Declaration.Coverage')=json_extract(result_json,'$.Coverage') AND json_type(result_json,'$.Cash')='object' AND json_type(result_json,'$.Cash.Evidence')='object' AND ((json_extract(result_json,'$.Cash.Evidence.State')='UNKNOWN' AND json_type(result_json,'$.Cash.Cents')='null') OR
 (json_extract(result_json,'$.Cash.Evidence.State')='DECLARED' AND json_type(result_json,'$.Cash.Cents')='integer' AND json_extract(result_json,'$.Cash.Cents')>=0)) AND (json_type(result_json,'$.DeclaredBalanceAtCutCents') IS NULL OR json_type(result_json,'$.DeclaredBalanceAtCutCents')='null' OR (json_type(result_json,'$.DeclaredBalanceAtCutCents')='integer' AND json_extract(result_json,'$.DeclaredBalanceAtCutCents')>=0))),0) ELSE 0 END=1),
 integration_admission TEXT NOT NULL DEFAULT 'PENDING' CHECK(integration_admission='PENDING'),
 financial_effect_cents INTEGER NOT NULL DEFAULT 0 CHECK(typeof(financial_effect_cents)='integer' AND financial_effect_cents=0),
 PRIMARY KEY(workspace_id,account_id,id), UNIQUE(workspace_id,account_id,version), UNIQUE(workspace_id,account_id,request_id),
 UNIQUE(workspace_id,account_id,id,version),
 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,account_id,previous_id,previous_version) REFERENCES investment_adoption_confirmations(workspace_id,account_id,id,version) ON DELETE RESTRICT,
 CHECK((version=1 AND previous_id IS NULL) OR (version>1 AND previous_id IS NOT NULL))
 );
CREATE TABLE investment_adoption_items (
 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL, confirmation_id TEXT NOT NULL,
 investment_id TEXT NOT NULL, position_id TEXT NOT NULL, reference_revision_id TEXT NOT NULL,
 represented_cents INTEGER NOT NULL CHECK(typeof(represented_cents)='integer' AND represented_cents>=0),
 economic_identity TEXT NOT NULL CHECK(length(trim(economic_identity)) BETWEEN 1 AND 200),
 identity_basis TEXT NOT NULL CHECK(length(trim(identity_basis)) BETWEEN 1 AND 500),
 PRIMARY KEY(workspace_id,investment_id), UNIQUE(workspace_id,account_id,economic_identity),
 FOREIGN KEY(workspace_id,account_id,confirmation_id) REFERENCES investment_adoption_confirmations(workspace_id,account_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,position_id) REFERENCES investment_valuations(workspace_id,investment_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,account_id,reference_revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,account_id,id) ON DELETE RESTRICT
 );
CREATE TABLE investment_financial_adjustments (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 revision_id TEXT NOT NULL, adjustment_id TEXT NOT NULL, position_id TEXT NOT NULL,
		 reverses_id TEXT, economic_date TEXT NOT NULL CHECK(COALESCE(typeof(economic_date)='text' AND length(CAST(economic_date AS BLOB))=10 AND
	 economic_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
	 CAST(substr(economic_date,1,4) AS INTEGER) BETWEEN 1 AND 9999 AND
	 CAST(substr(economic_date,6,2) AS INTEGER) BETWEEN 1 AND 12 AND
	 CAST(substr(economic_date,9,2) AS INTEGER) BETWEEN 1 AND CASE CAST(substr(economic_date,6,2) AS INTEGER)
	 WHEN 2 THEN 28+(CAST(substr(economic_date,1,4) AS INTEGER)%4=0 AND (CAST(substr(economic_date,1,4) AS INTEGER)%100!=0 OR CAST(substr(economic_date,1,4) AS INTEGER)%400=0))
	 WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30 ELSE 31 END,0)=1),
		 economic_at INTEGER NOT NULL CHECK(typeof(economic_at)='integer' AND economic_at=unixepoch(economic_date||' 23:59:59')),
		 PRIMARY KEY(workspace_id,account_id,adjustment_id), UNIQUE(adjustment_id), UNIQUE(workspace_id,reverses_id),
		 UNIQUE(workspace_id,investment_id,account_id,adjustment_id),
		 FOREIGN KEY(workspace_id,investment_id,account_id,revision_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,account_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,account_id,adjustment_id) REFERENCES account_balance_adjustments(workspace_id,account_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,account_id,reverses_id) REFERENCES investment_financial_adjustments(workspace_id,investment_id,account_id,adjustment_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,position_id) REFERENCES investment_valuations(workspace_id,investment_id,id) ON DELETE RESTRICT
		);
CREATE TABLE investment_financial_revisions (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 id TEXT NOT NULL, reference_revision_id TEXT NOT NULL, previous_id TEXT,
		 version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version>0),
		 request_id TEXT NOT NULL CHECK(length(request_id)=36),
		 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
		 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
		 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
		 chain_json TEXT NOT NULL CHECK(json_valid(chain_json)),
		 PRIMARY KEY(workspace_id,investment_id,id),
		 UNIQUE(workspace_id,investment_id,version), UNIQUE(workspace_id,investment_id,request_id),
		 UNIQUE(workspace_id,investment_id,account_id,id),
		 FOREIGN KEY(workspace_id,investment_id,account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,reference_revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,previous_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 CHECK((version=1 AND previous_id IS NULL) OR (version>1 AND previous_id IS NOT NULL))
		);
CREATE TABLE investment_purchase_funding (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, purchase_id TEXT NOT NULL,
		 transaction_id TEXT NOT NULL, revision_id TEXT NOT NULL,
		 amount_cents INTEGER NOT NULL CHECK(typeof(amount_cents)='integer' AND amount_cents>0), snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
		 PRIMARY KEY(workspace_id,investment_id,purchase_id,transaction_id),
		 FOREIGN KEY(workspace_id,transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,purchase_id) REFERENCES investment_purchases(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,revision_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT
		);
CREATE TABLE investment_purchases (
		id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		investment_id TEXT NOT NULL,
		purchase_date TEXT NOT NULL CHECK ((
		length(purchase_date) = 10 AND
		substr(purchase_date, 5, 1) = '-' AND
		substr(purchase_date, 8, 1) = '-' AND
		substr(purchase_date, 1, 4) NOT GLOB '*[^0-9]*' AND
		substr(purchase_date, 6, 2) NOT GLOB '*[^0-9]*' AND
		substr(purchase_date, 9, 2) NOT GLOB '*[^0-9]*' AND
		CAST(substr(purchase_date, 6, 2) AS INTEGER) BETWEEN 1 AND 12 AND
		CAST(substr(purchase_date, 9, 2) AS INTEGER) BETWEEN 1 AND 31
	)),
		principal_cents INTEGER NOT NULL CHECK (typeof(principal_cents) = 'integer' AND principal_cents > 0),
		note_reference TEXT NOT NULL DEFAULT '' CHECK (length(note_reference) <= 120),
		kind TEXT,
		institution TEXT,
		maturity_date TEXT CHECK (maturity_date IS NULL OR ((
		length(maturity_date) = 10 AND
		substr(maturity_date, 5, 1) = '-' AND
		substr(maturity_date, 8, 1) = '-' AND
		substr(maturity_date, 1, 4) NOT GLOB '*[^0-9]*' AND
		substr(maturity_date, 6, 2) NOT GLOB '*[^0-9]*' AND
		substr(maturity_date, 9, 2) NOT GLOB '*[^0-9]*' AND
		CAST(substr(maturity_date, 6, 2) AS INTEGER) BETWEEN 1 AND 12 AND
		CAST(substr(maturity_date, 9, 2) AS INTEGER) BETWEEN 1 AND 31
	))),
		tax_regime TEXT CHECK (tax_regime IN ('IR_REGRESSIVO', 'ISENTO_PF', 'MANUAL')),
		conditions_note TEXT,
		conditions_source TEXT NOT NULL CHECK (conditions_source IN ('LEGACY_UNKNOWN', 'RECORDED')),
		is_initial INTEGER NOT NULL CHECK (is_initial IN (0, 1)),
		created_by TEXT NOT NULL,
		created_at INTEGER NOT NULL DEFAULT (unixepoch()),
		PRIMARY KEY (workspace_id, id),
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
		FOREIGN KEY (investment_id, workspace_id) REFERENCES investments(id, workspace_id) ON DELETE CASCADE,
		FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT,
		CHECK (maturity_date IS NULL OR maturity_date >= purchase_date),
		CHECK ((conditions_source = 'LEGACY_UNKNOWN' AND is_initial = 1 AND kind IS NULL AND institution IS NULL AND maturity_date IS NULL AND tax_regime IS NULL AND conditions_note IS NULL)
			OR (conditions_source = 'RECORDED' AND kind IS NOT NULL AND length(trim(kind)) BETWEEN 1 AND 40 AND institution IS NOT NULL AND tax_regime IS NOT NULL AND conditions_note IS NOT NULL))
	);
CREATE TABLE investment_reference_coverage (
		 workspace_id TEXT NOT NULL,
		 investment_id TEXT NOT NULL,
		 revision_id TEXT NOT NULL,
		 operation_id TEXT NOT NULL,
		 kind TEXT NOT NULL CHECK(kind IN ('PURCHASE','WITHDRAWAL')),
		 purchase_id TEXT,
		 withdrawal_id TEXT,
		 PRIMARY KEY(workspace_id,investment_id,revision_id,kind,operation_id),
		 FOREIGN KEY(workspace_id,investment_id,revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,purchase_id) REFERENCES investment_purchases(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id) REFERENCES investment_withdrawals(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 CHECK((kind='PURCHASE' AND purchase_id IS NOT NULL AND purchase_id=operation_id AND withdrawal_id IS NULL) OR
		       (kind='WITHDRAWAL' AND withdrawal_id IS NOT NULL AND withdrawal_id=operation_id AND purchase_id IS NULL))
		);
CREATE TABLE investment_reference_revisions (
		 workspace_id TEXT NOT NULL,
		 investment_id TEXT NOT NULL,
		 account_id TEXT NOT NULL,
		 id TEXT NOT NULL,
		 reference_id TEXT NOT NULL,
		 previous_revision_id TEXT,
		 version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version>0),
		 expected_version INTEGER NOT NULL CHECK(typeof(expected_version)='integer' AND expected_version>=0 AND expected_version=version-1),
		 position_id TEXT NOT NULL,
		 position_version INTEGER NOT NULL CHECK(typeof(position_version)='integer' AND position_version>0),
		 economic_date TEXT NOT NULL CHECK(COALESCE(typeof(economic_date)='text' AND length(CAST(economic_date AS BLOB))=10 AND
	 economic_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
	 CAST(substr(economic_date,1,4) AS INTEGER) BETWEEN 1 AND 9999 AND
	 CAST(substr(economic_date,6,2) AS INTEGER) BETWEEN 1 AND 12 AND
	 CAST(substr(economic_date,9,2) AS INTEGER) BETWEEN 1 AND CASE CAST(substr(economic_date,6,2) AS INTEGER)
	 WHEN 2 THEN 28+(CAST(substr(economic_date,1,4) AS INTEGER)%4=0 AND (CAST(substr(economic_date,1,4) AS INTEGER)%100!=0 OR CAST(substr(economic_date,1,4) AS INTEGER)%400=0))
	 WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30 ELSE 31 END,0)=1),
		 represented_cents INTEGER CHECK(represented_cents IS NULL OR (typeof(represented_cents)='integer' AND represented_cents>=0)),
		 confirmed INTEGER NOT NULL CHECK(confirmed IN (0,1)),
		 day_closed INTEGER NOT NULL CHECK(day_closed IN (0,1)),
		 confirmed_at INTEGER,
		 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
		 request_id TEXT NOT NULL CHECK(length(request_id)=36),
		 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
		 origin TEXT NOT NULL DEFAULT 'MANUAL_REFERENCE' CHECK(origin='MANUAL_REFERENCE'),
		 integration_state TEXT NOT NULL DEFAULT 'PREPARED' CHECK(integration_state='PREPARED'),
		 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
		 coverage_token TEXT NOT NULL CHECK(length(coverage_token)=64),
		 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
		 PRIMARY KEY(workspace_id,investment_id,id),
		 UNIQUE(workspace_id,investment_id,version),
		 UNIQUE(workspace_id,investment_id,request_id),
		 UNIQUE(workspace_id,investment_id,id,version),
		 UNIQUE(workspace_id,investment_id,position_id,position_version),
		 FOREIGN KEY(investment_id,workspace_id) REFERENCES investments(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT,
		 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,position_id) REFERENCES investment_valuations(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,reference_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,previous_revision_id,expected_version) REFERENCES investment_reference_revisions(workspace_id,investment_id,id,version) ON DELETE RESTRICT,
		 CHECK((version=1 AND previous_revision_id IS NULL AND reference_id=id) OR (version>1 AND previous_revision_id IS NOT NULL)),
		 CHECK((confirmed=0 AND confirmed_at IS NULL) OR (confirmed=1 AND represented_cents IS NOT NULL AND confirmed_at IS NOT NULL AND confirmed_at=created_at AND typeof(confirmed_at)='integer'))
		);
CREATE TABLE investment_result_classifications (
 workspace_id TEXT NOT NULL, id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL,
 settlement_revision_id TEXT NOT NULL, application_id TEXT NOT NULL,
 settlement_version INTEGER NOT NULL CHECK(typeof(settlement_version)='integer' AND settlement_version>0),
 version INTEGER NOT NULL CHECK(version=1),
 transaction_id TEXT, category_id TEXT,
 principal_cents INTEGER CHECK(principal_cents IS NULL OR (typeof(principal_cents)='integer' AND principal_cents>=0)),
 result_cents INTEGER CHECK(result_cents IS NULL OR typeof(result_cents)='integer'),
 nature TEXT NOT NULL CHECK(nature IN ('REALIZED_GAIN','REALIZED_LOSS','ZERO','UNKNOWN')),
 state TEXT NOT NULL CHECK(state IN ('COMPLETED','UNAVAILABLE')),
 economic_date TEXT NOT NULL CHECK(COALESCE(typeof(economic_date)='text' AND length(CAST(economic_date AS BLOB))=10 AND
	 economic_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
	 CAST(substr(economic_date,1,4) AS INTEGER) BETWEEN 1 AND 9999 AND
	 CAST(substr(economic_date,6,2) AS INTEGER) BETWEEN 1 AND 12 AND
	 CAST(substr(economic_date,9,2) AS INTEGER) BETWEEN 1 AND CASE CAST(substr(economic_date,6,2) AS INTEGER)
	 WHEN 2 THEN 28+(CAST(substr(economic_date,1,4) AS INTEGER)%4=0 AND (CAST(substr(economic_date,1,4) AS INTEGER)%100!=0 OR CAST(substr(economic_date,1,4) AS INTEGER)%400=0))
	 WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30 ELSE 31 END,0)=1),
 economic_at INTEGER NOT NULL CHECK(typeof(economic_at)='integer' AND economic_at=unixepoch(economic_date||' 12:00:00')),
 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 request_id TEXT NOT NULL CHECK(length(request_id)=36),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)), result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 PRIMARY KEY(workspace_id,id), UNIQUE(workspace_id,request_id),
 UNIQUE(workspace_id,investment_id,withdrawal_id), UNIQUE(workspace_id,settlement_revision_id),
 UNIQUE(workspace_id,application_id), UNIQUE(workspace_id,transaction_id),
 FOREIGN KEY(workspace_id,investment_id,withdrawal_id,settlement_revision_id) REFERENCES investment_settlement_revisions(workspace_id,investment_id,withdrawal_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,application_id) REFERENCES investment_settlement_applications(workspace_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(category_id,workspace_id) REFERENCES categories(id,workspace_id) ON DELETE RESTRICT,
 CHECK((nature='UNKNOWN' AND state='UNAVAILABLE' AND principal_cents IS NULL AND result_cents IS NULL AND transaction_id IS NULL AND category_id IS NULL)
 OR (state='COMPLETED' AND principal_cents IS NOT NULL AND result_cents IS NOT NULL AND
 ((nature='ZERO' AND result_cents=0 AND transaction_id IS NULL AND category_id IS NULL)
 OR (nature='REALIZED_GAIN' AND result_cents>0 AND transaction_id IS NOT NULL AND category_id IS NOT NULL)
 OR (nature='REALIZED_LOSS' AND result_cents<0 AND result_cents>=-9223372036854775807 AND transaction_id IS NOT NULL AND category_id IS NOT NULL))))
 );
CREATE TABLE investment_settlement_applications (
 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL,
 id TEXT NOT NULL, settlement_revision_id TEXT NOT NULL, account_id TEXT NOT NULL,
 destination_account_id TEXT NOT NULL, reference_revision_id TEXT NOT NULL, financial_revision_id TEXT NOT NULL,
 settlement_version INTEGER NOT NULL CHECK(typeof(settlement_version)='integer' AND settlement_version>0),
 financial_version INTEGER NOT NULL CHECK(typeof(financial_version)='integer' AND financial_version>0),
 net_cents INTEGER NOT NULL CHECK(typeof(net_cents)='integer' AND net_cents>=0),
 patrimony_cents INTEGER NOT NULL CHECK(typeof(patrimony_cents)='integer' AND patrimony_cents>=0),
 prior_effect_cents INTEGER NOT NULL CHECK(typeof(prior_effect_cents)='integer'),
 residual_cents INTEGER NOT NULL CHECK(typeof(residual_cents)='integer'),
 adjustment_id TEXT,
 economic_date TEXT NOT NULL CHECK(COALESCE(typeof(economic_date)='text' AND length(CAST(economic_date AS BLOB))=10 AND
	 economic_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
	 CAST(substr(economic_date,1,4) AS INTEGER) BETWEEN 1 AND 9999 AND
	 CAST(substr(economic_date,6,2) AS INTEGER) BETWEEN 1 AND 12 AND
	 CAST(substr(economic_date,9,2) AS INTEGER) BETWEEN 1 AND CASE CAST(substr(economic_date,6,2) AS INTEGER)
	 WHEN 2 THEN 28+(CAST(substr(economic_date,1,4) AS INTEGER)%4=0 AND (CAST(substr(economic_date,1,4) AS INTEGER)%100!=0 OR CAST(substr(economic_date,1,4) AS INTEGER)%400=0))
	 WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30 ELSE 31 END,0)=1),
 economic_at INTEGER NOT NULL CHECK(typeof(economic_at)='integer' AND economic_at=unixepoch(economic_date||' 23:59:59')),
 coverage_date TEXT NOT NULL CHECK(COALESCE(typeof(coverage_date)='text' AND length(CAST(coverage_date AS BLOB))=10 AND
	 coverage_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
	 CAST(substr(coverage_date,1,4) AS INTEGER) BETWEEN 1 AND 9999 AND
	 CAST(substr(coverage_date,6,2) AS INTEGER) BETWEEN 1 AND 12 AND
	 CAST(substr(coverage_date,9,2) AS INTEGER) BETWEEN 1 AND CASE CAST(substr(coverage_date,6,2) AS INTEGER)
	 WHEN 2 THEN 28+(CAST(substr(coverage_date,1,4) AS INTEGER)%4=0 AND (CAST(substr(coverage_date,1,4) AS INTEGER)%100!=0 OR CAST(substr(coverage_date,1,4) AS INTEGER)%400=0))
	 WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30 ELSE 31 END,0)=1),
 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
 request_id TEXT NOT NULL CHECK(length(request_id)=36),
 expected_balance_version INTEGER NOT NULL CHECK(typeof(expected_balance_version)='integer' AND expected_balance_version>=0),
 expected_destination_version INTEGER NOT NULL CHECK(typeof(expected_destination_version)='integer' AND expected_destination_version>=0),
 state TEXT NOT NULL CHECK(state='FINANCIAL_APPLIED_CLASSIFICATION_PENDING'),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 proof_json TEXT NOT NULL CHECK(json_valid(proof_json)),
 PRIMARY KEY(workspace_id,id), UNIQUE(workspace_id,request_id),
 UNIQUE(workspace_id,investment_id,withdrawal_id),
 UNIQUE(workspace_id,investment_id,withdrawal_id,settlement_revision_id),
 UNIQUE(adjustment_id),
 FOREIGN KEY(workspace_id,investment_id,withdrawal_id,settlement_revision_id) REFERENCES investment_settlement_revisions(workspace_id,investment_id,withdrawal_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,account_id,financial_revision_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,account_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,reference_revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,account_id,adjustment_id) REFERENCES account_balance_adjustments(workspace_id,account_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(destination_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
 CHECK((residual_cents=0 AND adjustment_id IS NULL) OR (residual_cents!=0 AND adjustment_id IS NOT NULL))
 );
CREATE TABLE investment_settlement_prior_effects (
 workspace_id TEXT NOT NULL, application_id TEXT NOT NULL, account_id TEXT NOT NULL,
 adjustment_id TEXT, transaction_id TEXT,
 delta_cents INTEGER NOT NULL CHECK(typeof(delta_cents)='integer' AND delta_cents!=0),
 evidence TEXT NOT NULL CHECK(length(trim(evidence)) BETWEEN 1 AND 2000),
 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
 CHECK((adjustment_id IS NULL)!=(transaction_id IS NULL)),
 UNIQUE(workspace_id,adjustment_id), UNIQUE(workspace_id,transaction_id),
 FOREIGN KEY(workspace_id,application_id) REFERENCES investment_settlement_applications(workspace_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,account_id,adjustment_id) REFERENCES account_balance_adjustments(workspace_id,account_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT
 );
CREATE TABLE investment_settlement_receipts (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL, revision_id TEXT NOT NULL,
		 transaction_id TEXT NOT NULL, source_account_id TEXT, destination_account_id TEXT NOT NULL,
		 amount_cents INTEGER NOT NULL CHECK(typeof(amount_cents)='integer' AND amount_cents>0),
		 evidence TEXT NOT NULL CHECK(length(trim(evidence)) BETWEEN 1 AND 2000),
		 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
		 PRIMARY KEY(workspace_id,investment_id,withdrawal_id,transaction_id),
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id,revision_id) REFERENCES investment_settlement_revisions(workspace_id,investment_id,withdrawal_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(source_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(destination_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT
		);
CREATE TABLE investment_settlement_revisions (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL,
		 id TEXT NOT NULL, previous_id TEXT, version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version>0),
		 account_id TEXT NOT NULL, destination_account_id TEXT NOT NULL,
		 economic_date TEXT NOT NULL CHECK(COALESCE(typeof(economic_date)='text' AND length(CAST(economic_date AS BLOB))=10 AND
	 economic_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
	 CAST(substr(economic_date,1,4) AS INTEGER) BETWEEN 1 AND 9999 AND
	 CAST(substr(economic_date,6,2) AS INTEGER) BETWEEN 1 AND 12 AND
	 CAST(substr(economic_date,9,2) AS INTEGER) BETWEEN 1 AND CASE CAST(substr(economic_date,6,2) AS INTEGER)
	 WHEN 2 THEN 28+(CAST(substr(economic_date,1,4) AS INTEGER)%4=0 AND (CAST(substr(economic_date,1,4) AS INTEGER)%100!=0 OR CAST(substr(economic_date,1,4) AS INTEGER)%400=0))
	 WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30 ELSE 31 END,0)=1),
		 gross_cents INTEGER CHECK(gross_cents IS NULL OR (typeof(gross_cents)='integer' AND gross_cents>=0)),
		 ir_cents INTEGER CHECK(ir_cents IS NULL OR (typeof(ir_cents)='integer' AND ir_cents>=0)),
		 iof_cents INTEGER CHECK(iof_cents IS NULL OR (typeof(iof_cents)='integer' AND iof_cents>=0)),
		 fees_cents INTEGER CHECK(fees_cents IS NULL OR (typeof(fees_cents)='integer' AND fees_cents>=0)),
		 net_cents INTEGER NOT NULL CHECK(typeof(net_cents)='integer' AND net_cents>=0),
		 principal_cents INTEGER CHECK(principal_cents IS NULL OR (typeof(principal_cents)='integer' AND principal_cents>=0)),
		 result_cents INTEGER CHECK(result_cents IS NULL OR typeof(result_cents)='integer'),
		 patrimony_cents INTEGER CHECK(patrimony_cents IS NULL OR (typeof(patrimony_cents)='integer' AND patrimony_cents>=0)),
		 patrimony_evidence TEXT NOT NULL, receipt_evidence TEXT NOT NULL,
		 state TEXT NOT NULL CHECK(state IN ('PENDENTE','REGISTRADA','CONCILIADA')),
		 effects_state TEXT NOT NULL DEFAULT 'NOT_ACTIVATED' CHECK(effects_state='NOT_ACTIVATED'),
		 reference_revision_id TEXT, financial_revision_id TEXT,
		 request_id TEXT NOT NULL CHECK(length(request_id)=36),
		 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
		 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
		 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
		 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
		 withdrawal_json TEXT NOT NULL CHECK(json_valid(withdrawal_json)),
		 PRIMARY KEY(workspace_id,investment_id,withdrawal_id,id),
		 UNIQUE(workspace_id,investment_id,withdrawal_id,version), UNIQUE(workspace_id,request_id),
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id) REFERENCES investment_withdrawals(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT,
		 FOREIGN KEY(destination_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id,previous_id) REFERENCES investment_settlement_revisions(workspace_id,investment_id,withdrawal_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,reference_revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,financial_revision_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 CHECK((version=1 AND previous_id IS NULL) OR (version>1 AND previous_id IS NOT NULL)),
		 CHECK((principal_cents IS NULL AND result_cents IS NULL) OR (principal_cents IS NOT NULL AND result_cents IS NOT NULL AND result_cents=net_cents-principal_cents)),
		 CHECK(patrimony_cents IS NULL OR length(trim(patrimony_evidence))>0),
		 CHECK(state='PENDENTE' OR (gross_cents IS NOT NULL AND ir_cents IS NOT NULL AND iof_cents IS NOT NULL AND fees_cents IS NOT NULL AND patrimony_cents IS NOT NULL)),
		 CHECK(state!='CONCILIADA' OR length(trim(receipt_evidence))>0),
		 CHECK(gross_cents IS NULL OR ir_cents IS NULL OR iof_cents IS NULL OR fees_cents IS NULL OR
		  (ir_cents<=gross_cents AND iof_cents<=gross_cents-ir_cents AND fees_cents<=gross_cents-ir_cents-iof_cents AND net_cents=gross_cents-ir_cents-iof_cents-fees_cents))
		);
CREATE TABLE investment_valuations (
		id             TEXT    PRIMARY KEY,
		workspace_id   TEXT    NOT NULL,
		investment_id  TEXT    NOT NULL,
		valuation_date TEXT    NOT NULL CHECK ((
		length(valuation_date) = 10 AND
		substr(valuation_date, 5, 1) = '-' AND
		substr(valuation_date, 8, 1) = '-' AND
		substr(valuation_date, 1, 4) NOT GLOB '*[^0-9]*' AND
		substr(valuation_date, 6, 2) NOT GLOB '*[^0-9]*' AND
		substr(valuation_date, 9, 2) NOT GLOB '*[^0-9]*' AND
		CAST(substr(valuation_date, 6, 2) AS INTEGER) BETWEEN 1 AND 12 AND
		CAST(substr(valuation_date, 9, 2) AS INTEGER) BETWEEN 1 AND 31
	)),
		amount_cents   INTEGER NOT NULL CHECK (typeof(amount_cents) = 'integer' AND amount_cents >= 0),
		created_by     TEXT    NOT NULL,
		created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
		updated_by     TEXT    NULL,
		updated_at     INTEGER NULL,
		UNIQUE (workspace_id, investment_id, valuation_date),
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
		FOREIGN KEY (investment_id, workspace_id) REFERENCES investments(id, workspace_id) ON DELETE CASCADE,
		FOREIGN KEY (created_by)   REFERENCES users(id) ON DELETE RESTRICT,
		FOREIGN KEY (updated_by)   REFERENCES users(id) ON DELETE RESTRICT,
		CHECK (
			(updated_by IS NULL     AND updated_at IS NULL)
			OR
			(updated_by IS NOT NULL AND updated_at IS NOT NULL)
		)
	);
CREATE TABLE investment_withdrawal_allocations (
  workspace_id TEXT NOT NULL,
  investment_id TEXT NOT NULL,
  withdrawal_id TEXT NOT NULL,
  purchase_id TEXT NOT NULL,
  received_cents INTEGER NOT NULL CHECK(typeof(received_cents) = 'integer' AND received_cents >= 0),
  principal_cents INTEGER CHECK(principal_cents IS NULL OR (typeof(principal_cents) = 'integer' AND principal_cents >= 0)),
  closes_purchase INTEGER NOT NULL CHECK(closes_purchase IN (0,1)),
  PRIMARY KEY(workspace_id, investment_id, withdrawal_id, purchase_id),
  FOREIGN KEY(workspace_id, investment_id, withdrawal_id) REFERENCES investment_withdrawals(workspace_id, investment_id, id) ON DELETE CASCADE,
  FOREIGN KEY(workspace_id, investment_id, purchase_id) REFERENCES investment_purchases(workspace_id, investment_id, id) ON DELETE RESTRICT,
  CHECK(closes_purchase = 1 OR received_cents > 0)
 );
CREATE TABLE investment_withdrawals (
  id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  investment_id TEXT NOT NULL,
  -- Explicit Gregorian calendar arithmetic avoids SQLite date normalization.
  withdrawal_date TEXT NOT NULL CHECK(COALESCE(
   typeof(withdrawal_date) = 'text' AND
   length(CAST(withdrawal_date AS BLOB)) = 10 AND
   withdrawal_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
   CAST(substr(withdrawal_date, 1, 4) AS INTEGER) BETWEEN 1 AND 9999 AND
   CAST(substr(withdrawal_date, 6, 2) AS INTEGER) BETWEEN 1 AND 12 AND
   CAST(substr(withdrawal_date, 9, 2) AS INTEGER) BETWEEN 1 AND
    CASE CAST(substr(withdrawal_date, 6, 2) AS INTEGER)
     WHEN 2 THEN 28 + (CAST(substr(withdrawal_date, 1, 4) AS INTEGER) % 4 = 0 AND
      (CAST(substr(withdrawal_date, 1, 4) AS INTEGER) % 100 != 0 OR
       CAST(substr(withdrawal_date, 1, 4) AS INTEGER) % 400 = 0))
     WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30
     ELSE 31
    END,
   0) = 1),
  received_cents INTEGER NOT NULL CHECK(typeof(received_cents) = 'integer' AND received_cents >= 0),
  mode TEXT NOT NULL CHECK(mode IN ('PARTIAL','TOTAL')),
  created_by TEXT NOT NULL,
  request_id TEXT NOT NULL CHECK(length(request_id) = 36),
  note TEXT NOT NULL DEFAULT '' CHECK(length(note) <= 2000),
  created_at INTEGER NOT NULL DEFAULT(unixepoch()),
  PRIMARY KEY(workspace_id, investment_id, id),
  UNIQUE(workspace_id, investment_id, created_by, request_id),
  FOREIGN KEY(investment_id, workspace_id) REFERENCES investments(id, workspace_id) ON DELETE CASCADE,
  FOREIGN KEY(created_by) REFERENCES users(id) ON DELETE RESTRICT,
  CHECK(mode = 'TOTAL' OR received_cents > 0)
 );
CREATE TABLE investments (
		id              TEXT    PRIMARY KEY,
		workspace_id    TEXT    NOT NULL,
		account_id      TEXT    NOT NULL,
		name            TEXT    NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
		institution     TEXT    NOT NULL DEFAULT '',
		kind            TEXT    NOT NULL CHECK (typeof(kind) = 'text' AND length(trim(kind)) BETWEEN 1 AND 40),
		principal_cents INTEGER NOT NULL CHECK (typeof(principal_cents) = 'integer' AND principal_cents > 0),
		tax_regime      TEXT    NOT NULL CHECK (tax_regime IN ('IR_REGRESSIVO', 'ISENTO_PF', 'MANUAL')),
		applied_date    TEXT    NOT NULL CHECK ((
		length(applied_date) = 10 AND
		substr(applied_date, 5, 1) = '-' AND
		substr(applied_date, 8, 1) = '-' AND
		substr(applied_date, 1, 4) NOT GLOB '*[^0-9]*' AND
		substr(applied_date, 6, 2) NOT GLOB '*[^0-9]*' AND
		substr(applied_date, 9, 2) NOT GLOB '*[^0-9]*' AND
		CAST(substr(applied_date, 6, 2) AS INTEGER) BETWEEN 1 AND 12 AND
		CAST(substr(applied_date, 9, 2) AS INTEGER) BETWEEN 1 AND 31
	)),
		maturity_date   TEXT    NULL CHECK (maturity_date IS NULL OR ((
		length(maturity_date) = 10 AND
		substr(maturity_date, 5, 1) = '-' AND
		substr(maturity_date, 8, 1) = '-' AND
		substr(maturity_date, 1, 4) NOT GLOB '*[^0-9]*' AND
		substr(maturity_date, 6, 2) NOT GLOB '*[^0-9]*' AND
		substr(maturity_date, 9, 2) NOT GLOB '*[^0-9]*' AND
		CAST(substr(maturity_date, 6, 2) AS INTEGER) BETWEEN 1 AND 12 AND
		CAST(substr(maturity_date, 9, 2) AS INTEGER) BETWEEN 1 AND 31
	))),
		yield_note      TEXT    NOT NULL DEFAULT '',
		status          TEXT    NOT NULL DEFAULT 'ATIVA' CHECK (status IN ('ATIVA', 'RESGATADA')),
		redeemed_date   TEXT    NULL CHECK (redeemed_date IS NULL OR ((
		length(redeemed_date) = 10 AND
		substr(redeemed_date, 5, 1) = '-' AND
		substr(redeemed_date, 8, 1) = '-' AND
		substr(redeemed_date, 1, 4) NOT GLOB '*[^0-9]*' AND
		substr(redeemed_date, 6, 2) NOT GLOB '*[^0-9]*' AND
		substr(redeemed_date, 9, 2) NOT GLOB '*[^0-9]*' AND
		CAST(substr(redeemed_date, 6, 2) AS INTEGER) BETWEEN 1 AND 12 AND
		CAST(substr(redeemed_date, 9, 2) AS INTEGER) BETWEEN 1 AND 31
	))),
		redeemed_by     TEXT    NULL,
		archived_at     INTEGER NULL,
		created_by      TEXT    NOT NULL,
		created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
		updated_at      INTEGER NOT NULL DEFAULT (unixepoch()),
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
		FOREIGN KEY (account_id)   REFERENCES accounts(id)   ON DELETE RESTRICT,
		FOREIGN KEY (created_by)   REFERENCES users(id)      ON DELETE RESTRICT,
		FOREIGN KEY (redeemed_by)  REFERENCES users(id)      ON DELETE RESTRICT,
		CHECK (
			(status = 'ATIVA'     AND redeemed_date IS NULL     AND redeemed_by IS NULL)
			OR
			(status = 'RESGATADA' AND redeemed_date IS NOT NULL AND redeemed_by IS NOT NULL)
		),
		CHECK (archived_at IS NULL OR status = 'RESGATADA')
	);
CREATE TABLE invoice_payments (
					id             TEXT PRIMARY KEY,
					workspace_id   TEXT NOT NULL,
					invoice_id     TEXT NOT NULL,
					account_id     TEXT NOT NULL,
					transaction_id TEXT,
					amount_cents   INTEGER NOT NULL CHECK(amount_cents > 0),
					paid_at        INTEGER NOT NULL,
					note           TEXT,
					source         TEXT NOT NULL DEFAULT 'manual',
					reversed_at    INTEGER,
					created_by     TEXT,
					created_at     INTEGER NOT NULL, paid_date_civil TEXT, reversed_by TEXT, reversal_reason TEXT, reversal_transaction_id TEXT REFERENCES transactions(id) ON DELETE RESTRICT, reversed_date_civil TEXT,
					FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
					FOREIGN KEY (invoice_id) REFERENCES invoices(id) ON DELETE CASCADE,
					FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
				);
CREATE TABLE invoice_refunds (
			id           TEXT PRIMARY KEY,
			workspace_id TEXT    NOT NULL,
			invoice_id   TEXT    NOT NULL,
			amount_cents INTEGER NOT NULL CHECK (amount_cents > 0),
			description  TEXT    NOT NULL,
			created_by   TEXT    NOT NULL,
			created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
			reversed_at  INTEGER,
			reversed_by  TEXT,
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
			FOREIGN KEY (invoice_id)   REFERENCES invoices(id)   ON DELETE CASCADE,
			FOREIGN KEY (created_by)   REFERENCES users(id)      ON DELETE RESTRICT,
			FOREIGN KEY (reversed_by)  REFERENCES users(id)      ON DELETE RESTRICT,
			CHECK ((reversed_at IS NULL AND reversed_by IS NULL) OR (reversed_at IS NOT NULL AND reversed_by IS NOT NULL))
		);
CREATE TABLE invoices (
    id            TEXT PRIMARY KEY,
    account_id    TEXT NOT NULL,
    reference     TEXT NOT NULL,
    closing_date  INTEGER NOT NULL,
    due_date      INTEGER NOT NULL,
    status        TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'CLOSED', 'PAID')),
    paid_at       INTEGER,
    paid_amount   INTEGER,
    created_at    INTEGER NOT NULL DEFAULT (unixepoch()), closing_date_civil TEXT, due_date_civil TEXT,
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
    UNIQUE (account_id, reference)
);
CREATE TABLE mfa_setup_challenges (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    secret_enc   TEXT NOT NULL,
    expires_at   INTEGER NOT NULL,
    consumed_at  INTEGER,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE pre_auth_sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    method       TEXT NOT NULL CHECK (method IN ('TOTP')),
    expires_at   INTEGER NOT NULL,
    consumed_at  INTEGER,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()), remember_me INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE recurring_materialization_state (
    workspace_id       TEXT PRIMARY KEY,
    materialized_until INTEGER NOT NULL DEFAULT 0,
    last_attempt_at    INTEGER NOT NULL DEFAULT 0,
    last_success_at    INTEGER NOT NULL DEFAULT 0,
    last_error         TEXT,
    status             TEXT NOT NULL DEFAULT 'idle' CHECK (status IN ('idle', 'running', 'error')),
    locked_until       INTEGER NOT NULL DEFAULT 0,
    lock_owner         TEXT,
    created_at         INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at         INTEGER NOT NULL DEFAULT (unixepoch()), materialized_until_civil TEXT,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE TABLE recurring_occurrence_blocks (
    workspace_id          TEXT    NOT NULL,
    series_id             TEXT    NOT NULL,
    recurring_rule_id     TEXT    NOT NULL,
    recurrence_sequence   INTEGER NOT NULL CHECK (recurrence_sequence > 0),
    occurrence_date_civil TEXT    NOT NULL,
    invoice_id            TEXT    NOT NULL,
    invoice_reference     TEXT    NOT NULL,
    invoice_status        TEXT    NOT NULL CHECK (invoice_status IN ('CLOSED', 'PAID')),
    reason                TEXT    NOT NULL CHECK (reason = 'frozen_invoice'),
    created_at            INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, series_id, recurrence_sequence),
    UNIQUE (workspace_id, recurring_rule_id, recurrence_sequence)
);
CREATE TABLE recurring_occurrence_skips (
    workspace_id          TEXT    NOT NULL,
    series_id             TEXT    NOT NULL,
    recurrence_sequence   INTEGER NOT NULL CHECK (recurrence_sequence > 0),
    original_transaction_id TEXT,
    reason                TEXT    NOT NULL CHECK (reason IN ('single_delete')),
    deleted_by            TEXT,
    deleted_at            INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
);
CREATE TABLE recurring_rules (
    id                      TEXT PRIMARY KEY,
    workspace_id            TEXT    NOT NULL,
    user_id                 TEXT    NOT NULL,
    account_id              TEXT    NOT NULL,
    destination_account_id  TEXT,
    category_id             TEXT,
    type                    TEXT    NOT NULL CHECK (type IN ('EXPENSE', 'INCOME', 'TRANSFER')),
    amount                  INTEGER NOT NULL CHECK (amount > 0),
    description             TEXT    NOT NULL DEFAULT '',
    start_date              INTEGER NOT NULL,
    frequency               TEXT    NOT NULL CHECK (frequency IN ('DAILY', 'WEEKLY', 'BIWEEKLY', 'MONTHLY', 'BIMONTHLY', 'QUARTERLY', 'SEMIANNUAL', 'ANNUAL')),
    default_payment_status  TEXT    NOT NULL DEFAULT 'PENDING' CHECK (default_payment_status IN ('PAID', 'PENDING')),
    active                  INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    total_occurrences       INTEGER,
    series_id               TEXT,
    parent_rule_id          TEXT,
    effective_start_sequence INTEGER NOT NULL DEFAULT 1,
    effective_end_sequence  INTEGER,
    starts_at               INTEGER,
    end_date                INTEGER,
    contact_id              TEXT,
    due_date_policy         TEXT,
    due_date_offset_days    INTEGER,
    generated_until         INTEGER,
    created_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at              INTEGER NOT NULL DEFAULT (unixepoch()), start_date_civil TEXT, starts_at_civil TEXT, end_date_civil TEXT, generated_until_civil TEXT,
    FOREIGN KEY (workspace_id)           REFERENCES workspaces(id)  ON DELETE CASCADE,
    FOREIGN KEY (user_id)                REFERENCES users(id)       ON DELETE RESTRICT,
    FOREIGN KEY (account_id)             REFERENCES accounts(id)    ON DELETE RESTRICT,
    FOREIGN KEY (destination_account_id) REFERENCES accounts(id)    ON DELETE SET NULL,
    FOREIGN KEY (category_id)            REFERENCES categories(id)  ON DELETE SET NULL,
    FOREIGN KEY (series_id)              REFERENCES recurring_series(id) ON DELETE SET NULL,
    FOREIGN KEY (parent_rule_id)         REFERENCES recurring_rules(id)  ON DELETE SET NULL,
    FOREIGN KEY (contact_id)             REFERENCES contacts(id)         ON DELETE SET NULL
);
CREATE TABLE recurring_series (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL,
    user_id           TEXT,
    active            INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    total_occurrences INTEGER,
    created_at        INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at        INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id)      REFERENCES users(id)      ON DELETE SET NULL
);
CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at INTEGER NOT NULL DEFAULT (unixepoch())
	);
CREATE TABLE security_logs (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT,
    user_id      TEXT,
    event_type   TEXT NOT NULL,
    severity     TEXT NOT NULL,
    ip_address   TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT '',
    metadata     TEXT NOT NULL DEFAULT '{}',
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE SET NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL
);
CREATE TABLE sessions (
    id                 TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL,
    workspace_id       TEXT NOT NULL,
    token_hash         TEXT NOT NULL UNIQUE,
    expires_at         INTEGER NOT NULL,
    revoked_at         INTEGER,
    created_at         INTEGER NOT NULL DEFAULT (unixepoch()),
    reauthenticated_at INTEGER NOT NULL DEFAULT 0, is_remember INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE TABLE transaction_refunds (
			id             TEXT PRIMARY KEY,
			workspace_id   TEXT    NOT NULL,
			transaction_id TEXT    NOT NULL,
			amount_cents   INTEGER NOT NULL CHECK (amount_cents > 0),
			description    TEXT    NOT NULL,
			created_by     TEXT    NOT NULL,
			created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
			reversed_at    INTEGER,
			reversed_by    TEXT,
			FOREIGN KEY (workspace_id)   REFERENCES workspaces(id)   ON DELETE CASCADE,
			FOREIGN KEY (transaction_id) REFERENCES transactions(id) ON DELETE CASCADE,
			FOREIGN KEY (created_by)     REFERENCES users(id)        ON DELETE RESTRICT,
			FOREIGN KEY (reversed_by)    REFERENCES users(id)        ON DELETE RESTRICT,
			CHECK ((reversed_at IS NULL AND reversed_by IS NULL) OR (reversed_at IS NOT NULL AND reversed_by IS NOT NULL))
		);
CREATE TABLE transaction_status_history (
    id              TEXT PRIMARY KEY,
    transaction_id  TEXT NOT NULL,
    workspace_id    TEXT NOT NULL,
    user_id         TEXT NOT NULL,
    previous_status TEXT,
    new_status      TEXT,
    previous_paid_at INTEGER,
    new_paid_at     INTEGER,
    source          TEXT NOT NULL DEFAULT 'manual',
    created_at      INTEGER NOT NULL
);
CREATE TABLE transactions (
    id                      TEXT PRIMARY KEY,
    workspace_id            TEXT    NOT NULL,
    user_id                 TEXT    NOT NULL,
    account_id              TEXT    NOT NULL,
    destination_account_id  TEXT,
    category_id             TEXT,
    invoice_id              TEXT,
    invoice_override_id     TEXT,
    type                    TEXT    NOT NULL CHECK (type IN ('EXPENSE', 'INCOME', 'TRANSFER')),
    amount                  INTEGER NOT NULL CHECK (amount > 0),
    date                    INTEGER NOT NULL,
    description             TEXT    NOT NULL DEFAULT '',
    notes                   TEXT    NOT NULL DEFAULT '',
    attachment_path         TEXT    NOT NULL DEFAULT '',
    installment_number      INTEGER NOT NULL DEFAULT 1,
    total_installments      INTEGER NOT NULL DEFAULT 1,
    parent_id               TEXT,
    recurring_rule_id       TEXT,
    recurrence_sequence     INTEGER,
    series_id               TEXT,
    is_recurring_exception  INTEGER NOT NULL DEFAULT 0 CHECK (is_recurring_exception IN (0, 1)),
    recurring_exception_reason TEXT,
    -- LEGADO INATIVO: manter somente para compatibilidade e backfill da migration v11.
    -- @deprecated v20: use 'status' column exclusively.
    payment_status          TEXT    NOT NULL DEFAULT 'PAID' CHECK (payment_status IN ('PAID', 'PENDING')),
    status                  TEXT    NOT NULL DEFAULT 'paid' CHECK (status IN ('paid', 'pending')),
    due_date                INTEGER,
    paid_at                 INTEGER,
    scheduled_payment_at    INTEGER,
    scheduled_payment_created_by TEXT,
    scheduled_payment_executed_at INTEGER,
    contact_id              TEXT REFERENCES contacts(id) ON DELETE RESTRICT,
    created_at              INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at              INTEGER NOT NULL DEFAULT (unixepoch()), date_civil TEXT, due_date_civil TEXT, paid_date_civil TEXT, scheduled_payment_date_civil TEXT, effect_mode TEXT NOT NULL DEFAULT 'STANDARD' CHECK(effect_mode IN ('STANDARD','CLASSIFICATION_ONLY')),
    FOREIGN KEY (workspace_id)           REFERENCES workspaces(id)  ON DELETE CASCADE,
    FOREIGN KEY (user_id)                REFERENCES users(id)       ON DELETE RESTRICT,
    FOREIGN KEY (account_id)             REFERENCES accounts(id)    ON DELETE RESTRICT,
    FOREIGN KEY (destination_account_id) REFERENCES accounts(id)    ON DELETE SET NULL,
    FOREIGN KEY (category_id)            REFERENCES categories(id)  ON DELETE SET NULL,
    FOREIGN KEY (invoice_id)             REFERENCES invoices(id)    ON DELETE SET NULL,
    FOREIGN KEY (invoice_override_id)    REFERENCES invoices(id)    ON DELETE SET NULL,
    FOREIGN KEY (parent_id)              REFERENCES transactions(id) ON DELETE SET NULL,
    FOREIGN KEY (recurring_rule_id)      REFERENCES recurring_rules(id) ON DELETE SET NULL,
    FOREIGN KEY (series_id)              REFERENCES recurring_series(id) ON DELETE SET NULL
);
CREATE TABLE user_notification_dismissals (
    user_id       TEXT NOT NULL,
    workspace_id  TEXT NOT NULL,
    notification_key TEXT NOT NULL,
    dismissed_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (user_id, workspace_id, notification_key),
    FOREIGN KEY (user_id)      REFERENCES users(id)      ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE TABLE user_notifications (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    workspace_id TEXT,
    title      TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL DEFAULT '',
    type       TEXT NOT NULL DEFAULT '',
    dedupe_key TEXT,
    is_read    INTEGER NOT NULL DEFAULT 0 CHECK (is_read IN (0, 1)),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE users (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    email           TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    profile_photo_path TEXT NOT NULL DEFAULT '',
    default_workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending')),
    activation_token_hash TEXT,
    activation_expires_at INTEGER,
    must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1)),
    temporary_password_expires_at INTEGER,
    totp_enabled    INTEGER NOT NULL DEFAULT 0 CHECK (totp_enabled IN (0, 1)),
    totp_secret_enc TEXT,
    totp_backup_codes TEXT NOT NULL DEFAULT '[]',
    totp_enabled_at INTEGER,
    totp_last_used_step INTEGER NOT NULL DEFAULT -1,
    last_notifications_clear_at INTEGER NOT NULL DEFAULT 0,
    deleted_at      INTEGER NOT NULL DEFAULT 0,
    deleted_reason  TEXT,
    created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at      INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE TABLE workspace_members (
    workspace_id TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    role         TEXT NOT NULL DEFAULT 'USER' CHECK (role IN ('ADMIN', 'MANAGER', 'USER')),
    custom_permissions TEXT NOT NULL DEFAULT '[]',
    joined_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (workspace_id, user_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id)      REFERENCES users(id)      ON DELETE CASCADE
);
CREATE TABLE workspaces (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    type         TEXT NOT NULL DEFAULT 'personal' CHECK (type IN ('personal', 'business')),
    theme_token  TEXT NOT NULL DEFAULT 'violeta',
    company_name   TEXT,
    cnpj_cpf       TEXT,
    address        TEXT,
    phone          TEXT,
    logo_light_url TEXT,
    logo_dark_url  TEXT,
    smtp_host      TEXT,
    smtp_port      INTEGER NOT NULL DEFAULT 587,
    smtp_user      TEXT,
    smtp_pass      TEXT,
    notification_email TEXT,
    email_preferences TEXT NOT NULL DEFAULT '[]',
	monthly_income_reference INTEGER CHECK (monthly_income_reference IS NULL OR monthly_income_reference >= 0),
	monthly_total_cost_reference INTEGER CHECK (monthly_total_cost_reference IS NULL OR monthly_total_cost_reference >= 0),
	monthly_essential_cost_reference INTEGER CHECK (monthly_essential_cost_reference IS NULL OR monthly_essential_cost_reference >= 0),
    created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at     INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE UNIQUE INDEX idx_accounts_id_workspace ON accounts(id, workspace_id);
CREATE INDEX idx_accounts_workspace_archived ON accounts(workspace_id, archived_at);
CREATE INDEX idx_accounts_workspace_sort ON accounts(workspace_id, sort_order);
CREATE UNIQUE INDEX idx_adjustments_financial_owner ON account_balance_adjustments(workspace_id,account_id,id);
CREATE INDEX idx_adoption_items_confirmation ON investment_adoption_items(workspace_id,account_id,confirmation_id);
CREATE INDEX idx_auth_audit_events_type_created ON auth_audit_events(event_type, created_at DESC);
CREATE INDEX idx_auth_audit_events_user_created ON auth_audit_events(user_id, created_at DESC);
CREATE INDEX idx_auth_lockouts_locked_until ON auth_lockouts(locked_until);
CREATE INDEX idx_balance_adjustments_history ON account_balance_adjustments(workspace_id, account_id, created_at DESC);
CREATE INDEX idx_box_contribution_lifecycle_events_box_occurred ON box_contribution_lifecycle_events(box_id, occurred_at, id);
CREATE UNIQUE INDEX idx_box_virtual_ledger_automatic_recharge_month
		ON box_virtual_ledger(box_id, automatic_recharge_month)
		WHERE automatic_recharge_month IS NOT NULL
	;
CREATE INDEX idx_box_virtual_ledger_box ON box_virtual_ledger(box_id);
CREATE INDEX idx_box_virtual_ledger_box_reference_date ON box_virtual_ledger(box_id, reference_date);
CREATE INDEX idx_box_virtual_ledger_reversal_of ON box_virtual_ledger(reversal_of_ledger_id);
CREATE INDEX idx_box_virtual_ledger_source_transaction ON box_virtual_ledger(source_transaction_id);
CREATE INDEX idx_boxes_workspace             ON boxes(workspace_id);
CREATE INDEX idx_boxes_workspace_category    ON boxes(workspace_id, category_id);
CREATE UNIQUE INDEX idx_c2b_category_tenant ON categories(id,workspace_id);
CREATE INDEX idx_categories_parent ON categories(parent_id);
CREATE INDEX idx_categories_workspace ON categories(workspace_id);
CREATE UNIQUE INDEX idx_contacts_workspace_custom_client_id_unique ON contacts(workspace_id, custom_client_id);
CREATE INDEX idx_contacts_workspace_name_created ON contacts(workspace_id, name COLLATE NOCASE, created_at DESC);
CREATE INDEX idx_cost_limits_workspace       ON cost_limits(workspace_id);
CREATE INDEX idx_investment_adjustments_date ON investment_financial_adjustments(workspace_id,account_id,economic_at);
CREATE INDEX idx_investment_purchases_date ON investment_purchases(workspace_id, investment_id, purchase_date, id);
CREATE UNIQUE INDEX idx_investment_purchases_initial ON investment_purchases(workspace_id, investment_id) WHERE is_initial = 1;
CREATE UNIQUE INDEX idx_investment_purchases_owner ON investment_purchases(workspace_id, investment_id, id);
CREATE INDEX idx_investment_valuations_workspace_date ON investment_valuations(workspace_id, valuation_date);
CREATE INDEX idx_investment_withdrawals_date ON investment_withdrawals(workspace_id, investment_id, withdrawal_date, id);
CREATE INDEX idx_investment_withdrawals_purchase ON investment_withdrawal_allocations(workspace_id, investment_id, purchase_id);
CREATE UNIQUE INDEX idx_investments_id_workspace ON investments(id, workspace_id);
CREATE UNIQUE INDEX idx_investments_reference_owner ON investments(workspace_id,id,account_id);
CREATE INDEX idx_investments_workspace_account ON investments(workspace_id, account_id);
CREATE INDEX idx_investments_workspace_status ON investments(workspace_id, status, archived_at);
CREATE INDEX idx_invoice_payments_transaction ON invoice_payments(transaction_id);
CREATE INDEX idx_invoice_payments_workspace_account ON invoice_payments(workspace_id, account_id);
CREATE INDEX idx_invoice_payments_workspace_invoice ON invoice_payments(workspace_id, invoice_id);
CREATE INDEX idx_invoice_payments_workspace_paid_date_civil ON invoice_payments(workspace_id, paid_date_civil);
CREATE INDEX idx_invoice_refunds_invoice_active ON invoice_refunds(invoice_id, reversed_at);
CREATE INDEX idx_invoice_refunds_workspace_created ON invoice_refunds(workspace_id, created_at DESC);
CREATE INDEX idx_invoices_account            ON invoices(account_id);
CREATE INDEX idx_invoices_account_reference_status ON invoices(account_id, reference, status);
CREATE UNIQUE INDEX idx_invoices_account_reference_unique ON invoices(account_id, reference);
CREATE INDEX idx_invoices_closing_date_civil ON invoices(closing_date_civil);
CREATE INDEX idx_invoices_due_date_civil ON invoices(due_date_civil);
CREATE INDEX idx_invoices_status             ON invoices(status);
CREATE INDEX idx_mfa_setup_challenges_expires_at ON mfa_setup_challenges(expires_at);
CREATE INDEX idx_mfa_setup_challenges_user_id ON mfa_setup_challenges(user_id);
CREATE INDEX idx_pre_auth_sessions_expires_at ON pre_auth_sessions(expires_at);
CREATE INDEX idx_pre_auth_sessions_user_id ON pre_auth_sessions(user_id);
CREATE INDEX idx_purchase_funding_source ON investment_purchase_funding(workspace_id,transaction_id);
CREATE INDEX idx_recurring_materialization_state_locked_until ON recurring_materialization_state(locked_until);
CREATE INDEX idx_recurring_materialization_state_workspace_materialized_until_civil ON recurring_materialization_state(workspace_id, materialized_until_civil);
CREATE INDEX idx_recurring_rules_active      ON recurring_rules(active);
CREATE INDEX idx_recurring_rules_parent_rule_id ON recurring_rules(parent_rule_id);
CREATE INDEX idx_recurring_rules_start_date  ON recurring_rules(start_date);
CREATE INDEX idx_recurring_rules_workspace   ON recurring_rules(workspace_id);
CREATE INDEX idx_recurring_rules_workspace_end_date_civil ON recurring_rules(workspace_id, end_date_civil);
CREATE INDEX idx_recurring_rules_workspace_series_dates ON recurring_rules(workspace_id, series_id, starts_at, end_date);
CREATE INDEX idx_recurring_rules_workspace_series_start ON recurring_rules(workspace_id, series_id, effective_start_sequence);
CREATE INDEX idx_recurring_rules_workspace_start_date_civil ON recurring_rules(workspace_id, start_date_civil);
CREATE INDEX idx_recurring_rules_workspace_starts_at_civil ON recurring_rules(workspace_id, starts_at_civil);
CREATE INDEX idx_recurring_series_workspace ON recurring_series(workspace_id);
CREATE UNIQUE INDEX idx_reference_adoption_owner ON investment_reference_revisions(workspace_id,investment_id,account_id,id);
CREATE INDEX idx_reference_coverage_purchase ON investment_reference_coverage(workspace_id,investment_id,purchase_id);
CREATE INDEX idx_reference_coverage_withdrawal ON investment_reference_coverage(workspace_id,investment_id,withdrawal_id);
CREATE INDEX idx_security_logs_event_created ON security_logs(event_type, created_at DESC);
CREATE INDEX idx_security_logs_workspace_created ON security_logs(workspace_id, created_at DESC);
CREATE INDEX idx_sessions_expires_at     ON sessions(expires_at);
CREATE INDEX idx_sessions_user_id        ON sessions(user_id);
CREATE INDEX idx_settlement_application_date ON investment_settlement_applications(workspace_id,account_id,economic_at);
CREATE INDEX idx_settlement_receipt_source ON investment_settlement_receipts(workspace_id,transaction_id);
CREATE INDEX idx_transaction_refunds_transaction_active ON transaction_refunds(transaction_id, reversed_at);
CREATE INDEX idx_transaction_refunds_workspace_created ON transaction_refunds(workspace_id, created_at DESC);
CREATE INDEX idx_transactions_account        ON transactions(account_id);
CREATE INDEX idx_transactions_category       ON transactions(category_id);
CREATE INDEX idx_transactions_date           ON transactions(date);
CREATE UNIQUE INDEX idx_transactions_funding_owner ON transactions(workspace_id,id);
CREATE INDEX idx_transactions_invoice        ON transactions(invoice_id);
CREATE INDEX idx_transactions_parent         ON transactions(parent_id);
CREATE INDEX idx_transactions_recurring_rule ON transactions(recurring_rule_id);
CREATE INDEX idx_transactions_scheduled_due ON transactions(workspace_id, scheduled_payment_at, scheduled_payment_executed_at) WHERE scheduled_payment_at IS NOT NULL;
CREATE INDEX idx_transactions_type           ON transactions(type);
CREATE INDEX idx_transactions_workspace      ON transactions(workspace_id);
CREATE INDEX idx_transactions_workspace_category_type_date ON transactions(workspace_id, category_id, type, date);
CREATE INDEX idx_transactions_workspace_contact ON transactions(workspace_id, contact_id);
CREATE INDEX idx_transactions_workspace_date_civil ON transactions(workspace_id, date_civil);
CREATE INDEX idx_transactions_workspace_date_created ON transactions(workspace_id, date DESC, created_at DESC);
CREATE INDEX idx_transactions_workspace_due_date_civil ON transactions(workspace_id, due_date_civil);
CREATE INDEX idx_transactions_workspace_invoice_date ON transactions(workspace_id, invoice_id, date DESC);
CREATE INDEX idx_transactions_workspace_paid_date_civil ON transactions(workspace_id, paid_date_civil);
CREATE INDEX idx_transactions_workspace_parent ON transactions(workspace_id, parent_id);
CREATE INDEX idx_transactions_workspace_parent_date ON transactions(workspace_id, parent_id, date);
CREATE INDEX idx_transactions_workspace_recurring_date ON transactions(workspace_id, recurring_rule_id, date);
CREATE INDEX idx_transactions_workspace_recurring_sequence ON transactions(workspace_id, recurring_rule_id, recurrence_sequence);
CREATE UNIQUE INDEX idx_transactions_workspace_recurring_sequence_unique ON transactions(workspace_id, recurring_rule_id, recurrence_sequence) WHERE recurring_rule_id IS NOT NULL AND recurrence_sequence IS NOT NULL;
CREATE INDEX idx_transactions_workspace_scheduled_payment_date_civil ON transactions(workspace_id, scheduled_payment_date_civil);
CREATE INDEX idx_transactions_workspace_series ON transactions(workspace_id, series_id);
CREATE INDEX idx_transactions_workspace_series_date ON transactions(workspace_id, series_id, date);
CREATE INDEX idx_transactions_workspace_series_sequence ON transactions(workspace_id, series_id, recurrence_sequence);
CREATE INDEX idx_transactions_workspace_status_date ON transactions(workspace_id, status, date);
CREATE INDEX idx_transactions_workspace_status_type_due_created ON transactions(workspace_id, status, type, due_date, created_at);
CREATE INDEX idx_transactions_workspace_type_date ON transactions(workspace_id, type, date);
CREATE INDEX idx_tsh_transaction ON transaction_status_history(transaction_id);
CREATE INDEX idx_tsh_workspace_created ON transaction_status_history(workspace_id, created_at);
CREATE INDEX idx_user_notification_dismissals_workspace ON user_notification_dismissals(workspace_id, user_id);
CREATE UNIQUE INDEX idx_user_notifications_dedupe ON user_notifications(user_id, workspace_id, dedupe_key) WHERE dedupe_key IS NOT NULL AND dedupe_key <> '';
CREATE INDEX idx_user_notifications_user_read ON user_notifications(user_id, is_read);
CREATE INDEX idx_user_notifications_user_workspace_created ON user_notifications(user_id, workspace_id, created_at);
CREATE INDEX idx_users_activation_token_hash ON users(activation_token_hash);
CREATE UNIQUE INDEX idx_valuations_reference_owner ON investment_valuations(workspace_id,investment_id,id);
CREATE INDEX idx_workspace_members_user      ON workspace_members(user_id);
CREATE UNIQUE INDEX uq_invoice_payments_workspace_reversal_transaction
			ON invoice_payments(workspace_id, reversal_transaction_id)
			WHERE reversal_transaction_id IS NOT NULL;
CREATE UNIQUE INDEX uq_invoice_payments_workspace_transaction
			ON invoice_payments(workspace_id, transaction_id)
			WHERE transaction_id IS NOT NULL;
CREATE VIEW financial_transactions AS SELECT * FROM transactions WHERE effect_mode='STANDARD';
CREATE VIEW investment_adjustment_economic_dates AS
 SELECT workspace_id,account_id,adjustment_id,economic_at FROM investment_financial_adjustments
 UNION ALL SELECT workspace_id,account_id,adjustment_id,economic_at FROM investment_settlement_applications WHERE adjustment_id IS NOT NULL;
CREATE TRIGGER trg_accounts_balance_version
		AFTER UPDATE OF current_balance ON accounts
		BEGIN
			UPDATE accounts SET balance_version = OLD.balance_version + 1 WHERE id = NEW.id;
		END;
CREATE TRIGGER trg_accounts_balance_version_limit
		BEFORE UPDATE OF current_balance ON accounts
		WHEN OLD.balance_version >= 9223372036854775807
		BEGIN SELECT RAISE(ABORT, 'account balance version exhausted'); END;
CREATE TRIGGER trg_accounts_refund_workspace_identity
		BEFORE UPDATE OF workspace_id ON accounts
		FOR EACH ROW WHEN NEW.workspace_id IS NOT OLD.workspace_id
		BEGIN
			SELECT RAISE(ABORT, 'account workspace is immutable');
		END;
CREATE TRIGGER trg_adoption_attempt_insert BEFORE INSERT ON investment_adoption_attempts
 WHEN EXISTS(SELECT 1 FROM investment_adoption_attempts a WHERE a.workspace_id=NEW.workspace_id AND a.account_id=NEW.account_id AND a.id=NEW.id)
 BEGIN SELECT RAISE(ABORT,'immutable adoption attempt'); END;
CREATE TRIGGER trg_adoption_confirmation_insert BEFORE INSERT ON investment_adoption_confirmations
 WHEN EXISTS(SELECT 1 FROM investment_adoption_confirmations c WHERE c.workspace_id=NEW.workspace_id AND c.account_id=NEW.account_id AND (c.id=NEW.id OR c.version=NEW.version OR c.request_id=NEW.request_id))
 OR NEW.version!=(SELECT COALESCE(MAX(version),0)+1 FROM investment_adoption_confirmations WHERE workspace_id=NEW.workspace_id AND account_id=NEW.account_id)
 OR (NEW.version>1 AND NOT EXISTS(SELECT 1 FROM investment_adoption_confirmations c WHERE c.workspace_id=NEW.workspace_id AND c.account_id=NEW.account_id AND c.id=NEW.previous_id AND c.version=NEW.previous_version AND c.cut_date=NEW.cut_date))
 BEGIN SELECT RAISE(ABORT,'invalid adoption chain'); END;
CREATE TRIGGER trg_adoption_item_insert BEFORE INSERT ON investment_adoption_items
 WHEN EXISTS(SELECT 1 FROM investment_adoption_items i WHERE i.workspace_id=NEW.workspace_id AND (i.investment_id=NEW.investment_id OR (i.account_id=NEW.account_id AND i.economic_identity=NEW.economic_identity)))
 OR NOT EXISTS(SELECT 1 FROM investment_reference_revisions r JOIN investment_adoption_confirmations c ON c.workspace_id=NEW.workspace_id AND c.account_id=NEW.account_id AND c.id=NEW.confirmation_id
 WHERE r.workspace_id=NEW.workspace_id AND r.investment_id=NEW.investment_id AND r.account_id=NEW.account_id AND r.id=NEW.reference_revision_id AND r.position_id=NEW.position_id AND r.economic_date=c.cut_date AND r.confirmed=1 AND r.represented_cents=NEW.represented_cents AND r.represented_cents=json_extract(r.snapshot_json,'$.Position.amount_cents'))
 BEGIN SELECT RAISE(ABORT,'invalid adoption item'); END;
CREATE TRIGGER trg_adoption_reference_seal BEFORE INSERT ON investment_reference_revisions
 WHEN EXISTS(SELECT 1 FROM investment_adoption_items i WHERE i.workspace_id=NEW.workspace_id AND i.investment_id=NEW.investment_id)
 BEGIN SELECT RAISE(ABORT,'adopted reference sealed'); END;
CREATE TRIGGER trg_balance_adjustments_immutable
		BEFORE UPDATE ON account_balance_adjustments
		BEGIN SELECT RAISE(ABORT, 'account balance adjustment is immutable'); END;
CREATE TRIGGER trg_balance_adjustments_no_delete
		BEFORE DELETE ON account_balance_adjustments
		WHEN EXISTS (SELECT 1 FROM accounts WHERE id = OLD.account_id)
		AND EXISTS (SELECT 1 FROM workspaces WHERE id = OLD.workspace_id)
		BEGIN SELECT RAISE(ABORT, 'account balance adjustment is immutable'); END;
CREATE TRIGGER trg_balance_adjustments_validate
		BEFORE INSERT ON account_balance_adjustments
		WHEN NOT EXISTS (
			SELECT 1 FROM accounts a WHERE a.id = NEW.account_id AND a.workspace_id = NEW.workspace_id
			AND a.type != 'CREDIT_CARD' AND a.archived_at IS NULL
			AND a.balance_version = NEW.expected_version AND a.current_balance = NEW.before_balance
		)
		BEGIN SELECT RAISE(ABORT, 'stale account balance adjustment'); END;
CREATE TRIGGER trg_c2a_allocations_DELETE BEFORE DELETE ON investment_withdrawal_allocations WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_withdrawals w ON w.workspace_id=a.workspace_id AND w.investment_id=a.investment_id WHERE w.workspace_id=OLD.workspace_id AND w.investment_id=OLD.investment_id AND w.id=OLD.withdrawal_id AND w.withdrawal_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement allocation is sealed'); END;
CREATE TRIGGER trg_c2a_allocations_INSERT BEFORE INSERT ON investment_withdrawal_allocations WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_withdrawals w ON w.workspace_id=a.workspace_id AND w.investment_id=a.investment_id WHERE w.workspace_id=NEW.workspace_id AND w.investment_id=NEW.investment_id AND w.id=NEW.withdrawal_id AND w.withdrawal_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement allocation is sealed'); END;
CREATE TRIGGER trg_c2a_allocations_UPDATE BEFORE UPDATE ON investment_withdrawal_allocations WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_withdrawals w ON w.workspace_id=a.workspace_id AND w.investment_id=a.investment_id WHERE w.workspace_id=OLD.workspace_id AND w.investment_id=OLD.investment_id AND w.id=OLD.withdrawal_id AND w.withdrawal_date<=a.coverage_date) OR EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_withdrawals w ON w.workspace_id=a.workspace_id AND w.investment_id=a.investment_id WHERE w.workspace_id=NEW.workspace_id AND w.investment_id=NEW.investment_id AND w.id=NEW.withdrawal_id AND w.withdrawal_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement allocation is sealed'); END;
CREATE TRIGGER trg_c2a_effect_refund BEFORE INSERT ON transaction_refunds
 WHEN EXISTS(SELECT 1 FROM investment_settlement_prior_effects WHERE workspace_id=NEW.workspace_id AND transaction_id=NEW.transaction_id)
 BEGIN SELECT RAISE(ABORT,'applied settlement effect is sealed'); END;
CREATE TRIGGER trg_c2a_investment_purchases_DELETE BEFORE DELETE ON investment_purchases WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=OLD.workspace_id AND a.investment_id=OLD.investment_id AND OLD.purchase_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END;
CREATE TRIGGER trg_c2a_investment_purchases_INSERT BEFORE INSERT ON investment_purchases WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=NEW.workspace_id AND a.investment_id=NEW.investment_id AND NEW.purchase_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END;
CREATE TRIGGER trg_c2a_investment_purchases_UPDATE BEFORE UPDATE ON investment_purchases WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=OLD.workspace_id AND a.investment_id=OLD.investment_id AND OLD.purchase_date<=a.coverage_date) OR EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=NEW.workspace_id AND a.investment_id=NEW.investment_id AND NEW.purchase_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END;
CREATE TRIGGER trg_c2a_investment_valuations_DELETE BEFORE DELETE ON investment_valuations WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=OLD.workspace_id AND a.investment_id=OLD.investment_id AND OLD.valuation_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END;
CREATE TRIGGER trg_c2a_investment_valuations_INSERT BEFORE INSERT ON investment_valuations WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=NEW.workspace_id AND a.investment_id=NEW.investment_id AND NEW.valuation_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END;
CREATE TRIGGER trg_c2a_investment_valuations_UPDATE BEFORE UPDATE ON investment_valuations WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=OLD.workspace_id AND a.investment_id=OLD.investment_id AND OLD.valuation_date<=a.coverage_date) OR EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=NEW.workspace_id AND a.investment_id=NEW.investment_id AND NEW.valuation_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END;
CREATE TRIGGER trg_c2a_investment_withdrawals_DELETE BEFORE DELETE ON investment_withdrawals WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=OLD.workspace_id AND a.investment_id=OLD.investment_id AND OLD.withdrawal_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END;
CREATE TRIGGER trg_c2a_investment_withdrawals_INSERT BEFORE INSERT ON investment_withdrawals WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=NEW.workspace_id AND a.investment_id=NEW.investment_id AND NEW.withdrawal_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END;
CREATE TRIGGER trg_c2a_investment_withdrawals_UPDATE BEFORE UPDATE ON investment_withdrawals WHEN EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=OLD.workspace_id AND a.investment_id=OLD.investment_id AND OLD.withdrawal_date<=a.coverage_date) OR EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=NEW.workspace_id AND a.investment_id=NEW.investment_id AND NEW.withdrawal_date<=a.coverage_date) BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END;
CREATE TRIGGER trg_c2a_transaction_DELETE BEFORE DELETE ON transactions WHEN
 EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_settlement_receipts r ON r.workspace_id=a.workspace_id AND r.investment_id=a.investment_id AND r.withdrawal_id=a.withdrawal_id WHERE r.workspace_id=OLD.workspace_id AND r.transaction_id=OLD.id)
 OR EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_purchase_funding f ON f.workspace_id=a.workspace_id AND f.investment_id=a.investment_id WHERE f.workspace_id=OLD.workspace_id AND f.transaction_id=OLD.id)
 OR EXISTS(SELECT 1 FROM investment_settlement_prior_effects e WHERE e.workspace_id=OLD.workspace_id AND e.transaction_id=OLD.id) BEGIN SELECT RAISE(ABORT,'applied settlement transaction is sealed'); END;
CREATE TRIGGER trg_c2a_transaction_INSERT BEFORE INSERT ON transactions WHEN
 EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_settlement_receipts r ON r.workspace_id=a.workspace_id AND r.investment_id=a.investment_id AND r.withdrawal_id=a.withdrawal_id WHERE r.workspace_id=NEW.workspace_id AND r.transaction_id=NEW.id)
 OR EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_purchase_funding f ON f.workspace_id=a.workspace_id AND f.investment_id=a.investment_id WHERE f.workspace_id=NEW.workspace_id AND f.transaction_id=NEW.id)
 OR EXISTS(SELECT 1 FROM investment_settlement_prior_effects e WHERE e.workspace_id=NEW.workspace_id AND e.transaction_id=NEW.id) BEGIN SELECT RAISE(ABORT,'applied settlement transaction is sealed'); END;
CREATE TRIGGER trg_c2a_transaction_UPDATE BEFORE UPDATE OF id,workspace_id,account_id,destination_account_id,type,status,amount,date,date_civil,paid_at,paid_date_civil ON transactions WHEN
 EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_settlement_receipts r ON r.workspace_id=a.workspace_id AND r.investment_id=a.investment_id AND r.withdrawal_id=a.withdrawal_id WHERE r.workspace_id=OLD.workspace_id AND r.transaction_id=OLD.id)
 OR EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_purchase_funding f ON f.workspace_id=a.workspace_id AND f.investment_id=a.investment_id WHERE f.workspace_id=OLD.workspace_id AND f.transaction_id=OLD.id)
 OR EXISTS(SELECT 1 FROM investment_settlement_prior_effects e WHERE e.workspace_id=OLD.workspace_id AND e.transaction_id=OLD.id) BEGIN SELECT RAISE(ABORT,'applied settlement transaction is sealed'); END;
CREATE TRIGGER trg_c2b_classification_DELETE BEFORE DELETE ON investment_result_classifications BEGIN SELECT RAISE(ABORT,'immutable investment classification'); END;
CREATE TRIGGER trg_c2b_classification_UPDATE BEFORE UPDATE ON investment_result_classifications BEGIN SELECT RAISE(ABORT,'immutable investment classification'); END;
CREATE TRIGGER trg_c2b_origin BEFORE INSERT ON investment_result_classifications
 WHEN EXISTS(SELECT 1 FROM investment_result_classifications WHERE workspace_id=NEW.workspace_id AND
 (id=NEW.id OR request_id=NEW.request_id OR settlement_revision_id=NEW.settlement_revision_id OR application_id=NEW.application_id OR transaction_id=NEW.transaction_id))
 OR EXISTS(SELECT 1 FROM transactions WHERE workspace_id=NEW.workspace_id AND id=NEW.transaction_id)
 OR NOT EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_settlement_revisions s
 ON s.workspace_id=a.workspace_id AND s.investment_id=a.investment_id AND s.withdrawal_id=a.withdrawal_id AND s.id=a.settlement_revision_id
 WHERE a.workspace_id=NEW.workspace_id AND a.id=NEW.application_id AND a.investment_id=NEW.investment_id AND a.withdrawal_id=NEW.withdrawal_id
 AND a.settlement_revision_id=NEW.settlement_revision_id AND a.settlement_version=NEW.settlement_version AND s.version=NEW.settlement_version
 AND a.state='FINANCIAL_APPLIED_CLASSIFICATION_PENDING' AND s.state='CONCILIADA' AND s.economic_date=NEW.economic_date
 AND s.principal_cents IS NEW.principal_cents AND s.result_cents IS NEW.result_cents)
 BEGIN SELECT RAISE(ABORT,'invalid or duplicate investment classification'); END;
CREATE TRIGGER trg_c2b_refund BEFORE INSERT ON transaction_refunds
 WHEN EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=NEW.workspace_id AND t.id=NEW.transaction_id AND t.effect_mode='CLASSIFICATION_ONLY')
 BEGIN SELECT RAISE(ABORT,'investment classification cannot be refunded'); END;
CREATE TRIGGER trg_c2b_reserve BEFORE INSERT ON box_virtual_ledger
 WHEN EXISTS(SELECT 1 FROM boxes b JOIN transactions t ON t.workspace_id=b.workspace_id AND t.id=NEW.source_transaction_id
 WHERE b.id=NEW.box_id AND t.effect_mode='CLASSIFICATION_ONLY')
 BEGIN SELECT RAISE(ABORT,'investment classification cannot move reserves'); END;
CREATE TRIGGER trg_c2b_reserve_update BEFORE UPDATE ON box_virtual_ledger
 WHEN EXISTS(SELECT 1 FROM boxes b JOIN transactions t ON t.workspace_id=b.workspace_id AND t.id=NEW.source_transaction_id
 WHERE b.id=NEW.box_id AND t.effect_mode='CLASSIFICATION_ONLY')
 BEGIN SELECT RAISE(ABORT,'investment classification cannot move reserves'); END;
CREATE TRIGGER trg_c2b_transaction_delete BEFORE DELETE ON transactions
 WHEN OLD.effect_mode='CLASSIFICATION_ONLY'
 BEGIN SELECT RAISE(ABORT,'investment classification is immutable'); END;
CREATE TRIGGER trg_c2b_transaction_insert BEFORE INSERT ON transactions
 WHEN (NEW.effect_mode='STANDARD' AND EXISTS(SELECT 1 FROM investment_result_classifications c WHERE c.workspace_id=NEW.workspace_id AND c.transaction_id=NEW.id))
 OR (NEW.effect_mode='CLASSIFICATION_ONLY' AND NOT EXISTS(
 SELECT 1 FROM investment_result_classifications c JOIN investment_settlement_revisions s
 ON s.workspace_id=c.workspace_id AND s.investment_id=c.investment_id AND s.withdrawal_id=c.withdrawal_id AND s.id=c.settlement_revision_id
 JOIN categories cat ON cat.workspace_id=c.workspace_id AND cat.id=c.category_id
 WHERE c.workspace_id=NEW.workspace_id AND c.transaction_id=NEW.id AND c.state='COMPLETED'
 AND NEW.user_id=c.created_by AND NEW.category_id=c.category_id AND NEW.account_id=s.account_id
 AND NEW.type=cat.type AND ((c.nature='REALIZED_GAIN' AND NEW.type='INCOME' AND NEW.amount=c.result_cents)
 OR (c.nature='REALIZED_LOSS' AND NEW.type='EXPENSE' AND NEW.amount=-c.result_cents))
 AND typeof(NEW.amount)='integer' AND NEW.date=c.economic_at AND NEW.date_civil=c.economic_date
 AND NEW.created_at=c.created_at AND NEW.status='paid' AND NEW.paid_at IS NULL AND NEW.paid_date_civil IS NULL
 AND NEW.destination_account_id IS NULL AND NEW.invoice_id IS NULL AND NEW.invoice_override_id IS NULL
 AND NEW.parent_id IS NULL AND NEW.recurring_rule_id IS NULL AND NEW.series_id IS NULL AND NEW.recurrence_sequence IS NULL
 AND NEW.installment_number=1 AND NEW.total_installments=1 AND NEW.is_recurring_exception=0
 AND NEW.scheduled_payment_at IS NULL AND NEW.scheduled_payment_created_by IS NULL AND NEW.scheduled_payment_executed_at IS NULL
 AND NEW.due_date IS NULL AND NEW.due_date_civil IS NULL AND NEW.contact_id IS NULL))
 OR EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=NEW.workspace_id AND t.id=NEW.id AND t.effect_mode='CLASSIFICATION_ONLY')
 BEGIN SELECT RAISE(ABORT,'classification requires immutable settlement origin'); END;
CREATE TRIGGER trg_c2b_transaction_update BEFORE UPDATE ON transactions
 WHEN OLD.effect_mode='CLASSIFICATION_ONLY' OR NEW.effect_mode!=OLD.effect_mode
 BEGIN SELECT RAISE(ABORT,'investment classification is immutable'); END;
CREATE TRIGGER trg_financial_adjustment_insert BEFORE INSERT ON investment_financial_adjustments WHEN EXISTS(SELECT 1 FROM investment_financial_adjustments WHERE adjustment_id=NEW.adjustment_id OR (workspace_id=NEW.workspace_id AND reverses_id=NEW.reverses_id)) BEGIN SELECT RAISE(ABORT,'immutable integration link'); END;
CREATE TRIGGER trg_financial_revision_chain BEFORE INSERT ON investment_financial_revisions
		 WHEN EXISTS(SELECT 1 FROM investment_financial_revisions WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id AND (id=NEW.id OR request_id=NEW.request_id))
		 OR NEW.version != (SELECT COALESCE(MAX(version),0)+1 FROM investment_financial_revisions WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id)
		 OR (NEW.version>1 AND NOT EXISTS(SELECT 1 FROM investment_financial_revisions p WHERE p.workspace_id=NEW.workspace_id AND p.investment_id=NEW.investment_id AND p.id=NEW.previous_id AND p.version=NEW.version-1 AND p.reference_revision_id=NEW.reference_revision_id))
		 BEGIN SELECT RAISE(ABORT,'invalid financial revision chain'); END;
CREATE TRIGGER trg_investment_adoption_attempts_DELETE BEFORE DELETE ON investment_adoption_attempts BEGIN SELECT RAISE(ABORT,'immutable investment adoption'); END;
CREATE TRIGGER trg_investment_adoption_attempts_UPDATE BEFORE UPDATE ON investment_adoption_attempts BEGIN SELECT RAISE(ABORT,'immutable investment adoption'); END;
CREATE TRIGGER trg_investment_adoption_confirmations_DELETE BEFORE DELETE ON investment_adoption_confirmations BEGIN SELECT RAISE(ABORT,'immutable investment adoption'); END;
CREATE TRIGGER trg_investment_adoption_confirmations_UPDATE BEFORE UPDATE ON investment_adoption_confirmations BEGIN SELECT RAISE(ABORT,'immutable investment adoption'); END;
CREATE TRIGGER trg_investment_adoption_items_DELETE BEFORE DELETE ON investment_adoption_items BEGIN SELECT RAISE(ABORT,'immutable investment adoption'); END;
CREATE TRIGGER trg_investment_adoption_items_UPDATE BEFORE UPDATE ON investment_adoption_items BEGIN SELECT RAISE(ABORT,'immutable investment adoption'); END;
CREATE TRIGGER trg_investment_financial_adjustments_DELETE BEFORE DELETE ON investment_financial_adjustments BEGIN SELECT RAISE(ABORT,'immutable investment integration'); END;
CREATE TRIGGER trg_investment_financial_adjustments_UPDATE BEFORE UPDATE ON investment_financial_adjustments BEGIN SELECT RAISE(ABORT,'immutable investment integration'); END;
CREATE TRIGGER trg_investment_financial_revisions_DELETE BEFORE DELETE ON investment_financial_revisions BEGIN SELECT RAISE(ABORT,'immutable investment integration'); END;
CREATE TRIGGER trg_investment_financial_revisions_UPDATE BEFORE UPDATE ON investment_financial_revisions BEGIN SELECT RAISE(ABORT,'immutable investment integration'); END;
CREATE TRIGGER trg_investment_purchase_funding_DELETE BEFORE DELETE ON investment_purchase_funding BEGIN SELECT RAISE(ABORT,'immutable investment integration'); END;
CREATE TRIGGER trg_investment_purchase_funding_UPDATE BEFORE UPDATE ON investment_purchase_funding BEGIN SELECT RAISE(ABORT,'immutable investment integration'); END;
CREATE TRIGGER trg_investment_purchases_immutable
	BEFORE UPDATE ON investment_purchases FOR EACH ROW
	BEGIN SELECT RAISE(ABORT, 'investment purchase is immutable'); END;
CREATE TRIGGER trg_investment_purchases_validate_insert
	BEFORE INSERT ON investment_purchases FOR EACH ROW
	WHEN EXISTS (SELECT 1 FROM investment_purchases p WHERE p.workspace_id = NEW.workspace_id AND (p.id = NEW.id OR (NEW.is_initial = 1 AND p.investment_id = NEW.investment_id AND p.is_initial = 1)))
	OR NOT EXISTS (SELECT 1 FROM investments i WHERE i.workspace_id = NEW.workspace_id AND i.id = NEW.investment_id
		AND ((NEW.is_initial = 1 AND NEW.purchase_date = i.applied_date AND NEW.principal_cents = i.principal_cents AND NEW.created_by = i.created_by AND NEW.created_at = i.created_at)
		OR (NEW.is_initial = 0 AND NEW.conditions_source = 'RECORDED' AND i.status = 'ATIVA' AND i.archived_at IS NULL AND NEW.purchase_date >= i.applied_date)))
	OR COALESCE((SELECT SUM(p.principal_cents) FROM investment_purchases p WHERE p.workspace_id = NEW.workspace_id AND p.investment_id = NEW.investment_id), 0) > 9223372036854775807 - NEW.principal_cents
	BEGIN SELECT RAISE(ABORT, 'invalid investment purchase'); END;
CREATE TRIGGER trg_investment_reference_coverage_DELETE BEFORE DELETE ON investment_reference_coverage BEGIN SELECT RAISE(ABORT,'immutable investment reference'); END;
CREATE TRIGGER trg_investment_reference_coverage_UPDATE BEFORE UPDATE ON investment_reference_coverage BEGIN SELECT RAISE(ABORT,'immutable investment reference'); END;
CREATE TRIGGER trg_investment_reference_revisions_DELETE BEFORE DELETE ON investment_reference_revisions BEGIN SELECT RAISE(ABORT,'immutable investment reference'); END;
CREATE TRIGGER trg_investment_reference_revisions_UPDATE BEFORE UPDATE ON investment_reference_revisions BEGIN SELECT RAISE(ABORT,'immutable investment reference'); END;
CREATE TRIGGER trg_investment_settlement_applications_DELETE BEFORE DELETE ON investment_settlement_applications BEGIN SELECT RAISE(ABORT,'immutable settlement application'); END;
CREATE TRIGGER trg_investment_settlement_applications_UPDATE BEFORE UPDATE ON investment_settlement_applications BEGIN SELECT RAISE(ABORT,'immutable settlement application'); END;
CREATE TRIGGER trg_investment_settlement_prior_effects_DELETE BEFORE DELETE ON investment_settlement_prior_effects BEGIN SELECT RAISE(ABORT,'immutable settlement application'); END;
CREATE TRIGGER trg_investment_settlement_prior_effects_UPDATE BEFORE UPDATE ON investment_settlement_prior_effects BEGIN SELECT RAISE(ABORT,'immutable settlement application'); END;
CREATE TRIGGER trg_investment_settlement_receipts_DELETE BEFORE DELETE ON investment_settlement_receipts BEGIN SELECT RAISE(ABORT,'immutable settlement evidence'); END;
CREATE TRIGGER trg_investment_settlement_receipts_UPDATE BEFORE UPDATE ON investment_settlement_receipts BEGIN SELECT RAISE(ABORT,'immutable settlement evidence'); END;
CREATE TRIGGER trg_investment_settlement_revisions_DELETE BEFORE DELETE ON investment_settlement_revisions BEGIN SELECT RAISE(ABORT,'immutable settlement evidence'); END;
CREATE TRIGGER trg_investment_settlement_revisions_UPDATE BEFORE UPDATE ON investment_settlement_revisions BEGIN SELECT RAISE(ABORT,'immutable settlement evidence'); END;
CREATE TRIGGER trg_investment_valuations_immutable_keys
		BEFORE UPDATE ON investment_valuations
		FOR EACH ROW
		WHEN NEW.id             IS NOT OLD.id
		  OR NEW.workspace_id   IS NOT OLD.workspace_id
		  OR NEW.investment_id  IS NOT OLD.investment_id
		  OR NEW.valuation_date IS NOT OLD.valuation_date
		  OR NEW.created_by     IS NOT OLD.created_by
		  OR NEW.created_at     IS NOT OLD.created_at
		BEGIN
			SELECT RAISE(ABORT, 'investment valuation identity is immutable');
		END;
CREATE TRIGGER trg_investment_valuations_validate_insert
		BEFORE INSERT ON investment_valuations
		FOR EACH ROW
		WHEN NOT EXISTS (
			SELECT 1
			FROM investments i
			WHERE i.id = NEW.investment_id
			  AND i.workspace_id = NEW.workspace_id
			  AND i.status = 'ATIVA'
			  AND NEW.valuation_date >= i.applied_date
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid investment valuation');
		END;
CREATE TRIGGER trg_investment_valuations_validate_update
		BEFORE UPDATE ON investment_valuations
		FOR EACH ROW
		WHEN NOT EXISTS (
			SELECT 1
			FROM investments i
			WHERE i.id = NEW.investment_id
			  AND i.workspace_id = NEW.workspace_id
			  AND i.status = 'ATIVA'
			  AND NEW.valuation_date >= i.applied_date
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid investment valuation');
		END;
CREATE TRIGGER trg_investments_immutable_keys
		BEFORE UPDATE ON investments
		FOR EACH ROW
		WHEN NEW.id              IS NOT OLD.id
		  OR NEW.workspace_id    IS NOT OLD.workspace_id
		  OR NEW.account_id      IS NOT OLD.account_id
		  OR NEW.principal_cents IS NOT OLD.principal_cents
		  OR NEW.applied_date    IS NOT OLD.applied_date
		  OR NEW.created_by      IS NOT OLD.created_by
		  OR NEW.created_at      IS NOT OLD.created_at
		BEGIN
			SELECT RAISE(ABORT, 'investment identity is immutable');
		END;
CREATE TRIGGER trg_investments_initial_purchase
	AFTER INSERT ON investments FOR EACH ROW
	BEGIN
		INSERT INTO investment_purchases (id, workspace_id, investment_id, purchase_date, principal_cents, kind, institution, maturity_date, tax_regime, conditions_note, conditions_source, is_initial, created_by, created_at)
		VALUES ('initial:' || NEW.id, NEW.workspace_id, NEW.id, NEW.applied_date, NEW.principal_cents, NEW.kind, NEW.institution, NEW.maturity_date, NEW.tax_regime, NEW.yield_note, 'RECORDED', 1, NEW.created_by, NEW.created_at);
	END;
CREATE TRIGGER trg_investments_validate_insert
		BEFORE INSERT ON investments
		FOR EACH ROW
		WHEN NOT EXISTS (
			SELECT 1
			FROM accounts a
			WHERE a.id = NEW.account_id
			  AND a.workspace_id = NEW.workspace_id
			  AND a.type = 'INVESTMENT'
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid investment account');
		END;
CREATE TRIGGER trg_invoice_refunds_immutable_update
		BEFORE UPDATE OF workspace_id, invoice_id, amount_cents, description, created_by, created_at ON invoice_refunds
		FOR EACH ROW
		BEGIN
			SELECT RAISE(ABORT, 'invoice refund audit fields are immutable');
		END;
CREATE TRIGGER trg_invoice_refunds_no_delete
		BEFORE DELETE ON invoice_refunds
		FOR EACH ROW
		BEGIN
			SELECT RAISE(ABORT, 'invoice refunds must be reversed, not deleted');
		END;
CREATE TRIGGER trg_invoice_refunds_no_overpay
		BEFORE INSERT ON invoice_refunds
		FOR EACH ROW
		WHEN NEW.reversed_at IS NULL AND NEW.amount_cents + COALESCE((
			SELECT SUM(ir.amount_cents)
			FROM invoice_refunds ir
			WHERE ir.workspace_id = NEW.workspace_id
			  AND ir.invoice_id = NEW.invoice_id
			  AND ir.reversed_at IS NULL
		), 0) > COALESCE((
			SELECT SUM(t.amount - COALESCE((
				SELECT SUM(tr.amount_cents)
				FROM transaction_refunds tr
				WHERE tr.workspace_id = t.workspace_id
				  AND tr.transaction_id = t.id
				  AND tr.reversed_at IS NULL
			), 0))
			FROM transactions t
			WHERE t.workspace_id = NEW.workspace_id
			  AND t.invoice_id = NEW.invoice_id
			  AND t.type = 'EXPENSE'
		), 0) - COALESCE((
			SELECT CASE
				WHEN EXISTS (
					SELECT 1 FROM invoice_payments ip_lineage
					WHERE ip_lineage.workspace_id = NEW.workspace_id
					  AND ip_lineage.invoice_id = NEW.invoice_id
				) THEN COALESCE((
					SELECT SUM(ip_active.amount_cents)
					FROM invoice_payments ip_active
					WHERE ip_active.workspace_id = NEW.workspace_id
					  AND ip_active.invoice_id = NEW.invoice_id
					  AND ip_active.reversed_at IS NULL
				), 0)
				ELSE COALESCE(i.paid_amount, 0)
			END
			FROM invoices i
			WHERE i.id = NEW.invoice_id
		), 0)
		BEGIN
			SELECT RAISE(ABORT, 'invoice refund exceeds pending balance');
		END;
CREATE TRIGGER trg_invoice_refunds_validate_insert
		BEFORE INSERT ON invoice_refunds
		FOR EACH ROW
		WHEN NEW.reversed_at IS NULL AND NOT EXISTS (
			SELECT 1
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE i.id = NEW.invoice_id
			  AND a.workspace_id = NEW.workspace_id
			  AND a.type = 'CREDIT_CARD'
			  AND i.status IN ('OPEN', 'CLOSED')
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid invoice refund');
		END;
CREATE TRIGGER trg_invoices_refund_account_identity
		BEFORE UPDATE OF account_id ON invoices
		FOR EACH ROW WHEN NEW.account_id IS NOT OLD.account_id
		BEGIN
			SELECT RAISE(ABORT, 'invoice account is immutable');
		END;
CREATE TRIGGER trg_purchase_funding_insert BEFORE INSERT ON investment_purchase_funding WHEN EXISTS(SELECT 1 FROM investment_purchase_funding WHERE workspace_id=NEW.workspace_id AND transaction_id=NEW.transaction_id AND investment_id=NEW.investment_id AND purchase_id=NEW.purchase_id) BEGIN SELECT RAISE(ABORT,'immutable purchase funding'); END;
CREATE TRIGGER trg_recurring_occurrence_blocks_immutable_delete
BEFORE DELETE ON recurring_occurrence_blocks
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence block is immutable');
END;
CREATE TRIGGER trg_recurring_occurrence_blocks_immutable_update
BEFORE UPDATE ON recurring_occurrence_blocks
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence block is immutable');
END;
CREATE TRIGGER trg_recurring_occurrence_blocks_no_skip
BEFORE INSERT ON recurring_occurrence_blocks
WHEN EXISTS (
    SELECT 1 FROM recurring_occurrence_skips s
    WHERE s.workspace_id = NEW.workspace_id
      AND s.series_id = NEW.series_id
      AND s.recurrence_sequence = NEW.recurrence_sequence
)
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: manual skip exists');
END;
CREATE TRIGGER trg_recurring_occurrence_blocks_no_transaction
BEFORE INSERT ON recurring_occurrence_blocks
WHEN EXISTS (
    SELECT 1 FROM transactions t
    WHERE t.workspace_id = NEW.workspace_id
      AND t.series_id = NEW.series_id
      AND t.recurrence_sequence = NEW.recurrence_sequence
)
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: transaction exists');
END;
CREATE TRIGGER trg_recurring_occurrence_skips_no_block_insert
BEFORE INSERT ON recurring_occurrence_skips
WHEN EXISTS (
    SELECT 1 FROM recurring_occurrence_blocks b
    WHERE b.workspace_id = NEW.workspace_id
      AND b.series_id = NEW.series_id
      AND b.recurrence_sequence = NEW.recurrence_sequence
)
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: frozen block exists');
END;
CREATE TRIGGER trg_recurring_occurrence_skips_no_block_update
BEFORE UPDATE OF workspace_id, series_id, recurrence_sequence ON recurring_occurrence_skips
WHEN EXISTS (
    SELECT 1 FROM recurring_occurrence_blocks b
    WHERE b.workspace_id = NEW.workspace_id
      AND b.series_id = NEW.series_id
      AND b.recurrence_sequence = NEW.recurrence_sequence
)
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: frozen block exists');
END;
CREATE TRIGGER trg_recurring_rules_no_active_parent_insert
BEFORE INSERT ON recurring_rules
WHEN NEW.parent_rule_id IS NOT NULL AND (
    SELECT active FROM recurring_rules WHERE id = NEW.parent_rule_id
) = 1
BEGIN
    SELECT RAISE(ABORT, 'active rule cannot have children');
END;
CREATE TRIGGER trg_recurring_rules_no_active_parent_update
BEFORE UPDATE OF parent_rule_id ON recurring_rules
WHEN NEW.parent_rule_id IS NOT NULL
 AND NEW.parent_rule_id != COALESCE(OLD.parent_rule_id, '')
 AND (
    SELECT active FROM recurring_rules WHERE id = NEW.parent_rule_id
) = 1
BEGIN
    SELECT RAISE(ABORT, 'active rule cannot have children');
END;
CREATE TRIGGER trg_recurring_rules_no_reactivation_with_children
BEFORE UPDATE OF active ON recurring_rules
WHEN NEW.active = 1 AND OLD.active = 0 AND (
    SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id = OLD.id
) > 0
BEGIN
    SELECT RAISE(ABORT, 'active rule cannot have children');
END;
CREATE TRIGGER trg_reference_coverage_insert BEFORE INSERT ON investment_reference_coverage
		 WHEN EXISTS(SELECT 1 FROM investment_reference_coverage c WHERE c.workspace_id=NEW.workspace_id AND c.investment_id=NEW.investment_id AND c.revision_id=NEW.revision_id AND c.kind=NEW.kind AND c.operation_id=NEW.operation_id)
		 BEGIN SELECT RAISE(ABORT,'immutable reference coverage'); END;
CREATE TRIGGER trg_reference_revision_insert BEFORE INSERT ON investment_reference_revisions
		 WHEN EXISTS(SELECT 1 FROM investment_reference_revisions r WHERE r.workspace_id=NEW.workspace_id AND r.investment_id=NEW.investment_id AND
		 (r.id=NEW.id OR r.version=NEW.version OR r.request_id=NEW.request_id OR (r.position_id=NEW.position_id AND r.position_version=NEW.position_version)))
		 OR (NEW.version>1 AND NOT EXISTS(SELECT 1 FROM investment_reference_revisions p WHERE p.workspace_id=NEW.workspace_id AND p.investment_id=NEW.investment_id AND p.id=NEW.previous_revision_id AND p.reference_id=NEW.reference_id AND p.version=NEW.expected_version))
		 BEGIN SELECT RAISE(ABORT,'invalid reference revision chain'); END;
CREATE TRIGGER trg_settlement_application_insert BEFORE INSERT ON investment_settlement_applications
 WHEN EXISTS(SELECT 1 FROM investment_settlement_applications WHERE workspace_id=NEW.workspace_id AND (id=NEW.id OR request_id=NEW.request_id OR (investment_id=NEW.investment_id AND withdrawal_id=NEW.withdrawal_id)))
 OR EXISTS(SELECT 1 FROM investment_financial_adjustments WHERE adjustment_id=NEW.adjustment_id)
 OR EXISTS(SELECT 1 FROM investment_settlement_prior_effects WHERE adjustment_id=NEW.adjustment_id)
 BEGIN SELECT RAISE(ABORT,'settlement already applied'); END;
CREATE TRIGGER trg_settlement_chain BEFORE INSERT ON investment_settlement_revisions
		 WHEN EXISTS(SELECT 1 FROM investment_settlement_revisions WHERE workspace_id=NEW.workspace_id AND (request_id=NEW.request_id OR id=NEW.id))
		 OR NEW.version!=(SELECT COALESCE(MAX(version),0)+1 FROM investment_settlement_revisions WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id AND withdrawal_id=NEW.withdrawal_id)
		 OR (NEW.version>1 AND NOT EXISTS(SELECT 1 FROM investment_settlement_revisions p WHERE p.workspace_id=NEW.workspace_id AND p.investment_id=NEW.investment_id AND p.withdrawal_id=NEW.withdrawal_id AND p.id=NEW.previous_id AND p.version=NEW.version-1 AND p.state!='CONCILIADA'))
		 BEGIN SELECT RAISE(ABORT,'invalid settlement revision chain'); END;
CREATE TRIGGER trg_settlement_effect_insert BEFORE INSERT ON investment_settlement_prior_effects
 WHEN EXISTS(SELECT 1 FROM investment_settlement_prior_effects WHERE workspace_id=NEW.workspace_id AND (adjustment_id=NEW.adjustment_id OR transaction_id=NEW.transaction_id))
 OR EXISTS(SELECT 1 FROM investment_financial_adjustments WHERE adjustment_id=NEW.adjustment_id)
 OR EXISTS(SELECT 1 FROM investment_settlement_applications WHERE adjustment_id=NEW.adjustment_id)
 BEGIN SELECT RAISE(ABORT,'effect already attributed'); END;
CREATE TRIGGER trg_settlement_protect_financial_chain BEFORE INSERT ON investment_financial_revisions
 WHEN EXISTS(SELECT 1 FROM investment_settlement_applications WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id)
 BEGIN SELECT RAISE(ABORT,'applied settlement requires coordinated compensation'); END;
CREATE TRIGGER trg_settlement_protect_reference BEFORE INSERT ON investment_reference_revisions
 WHEN EXISTS(SELECT 1 FROM investment_settlement_applications WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id)
 BEGIN SELECT RAISE(ABORT,'applied settlement reference is sealed'); END;
CREATE TRIGGER trg_settlement_receipt_capacity BEFORE INSERT ON investment_settlement_receipts
		 WHEN NEW.amount_cents > (SELECT amount FROM transactions WHERE workspace_id=NEW.workspace_id AND id=NEW.transaction_id) -
		 COALESCE((SELECT SUM(amount_cents) FROM investment_settlement_receipts WHERE workspace_id=NEW.workspace_id AND transaction_id=NEW.transaction_id),0)
		 OR EXISTS(SELECT 1 FROM investment_settlement_receipts WHERE workspace_id=NEW.workspace_id AND transaction_id=NEW.transaction_id AND snapshot_json!=NEW.snapshot_json)
		 BEGIN SELECT RAISE(ABORT,'settlement receipt capacity or evidence conflict'); END;
CREATE TRIGGER trg_settlement_receipt_insert BEFORE INSERT ON investment_settlement_receipts
		 WHEN EXISTS(SELECT 1 FROM investment_settlement_receipts WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id AND withdrawal_id=NEW.withdrawal_id AND transaction_id=NEW.transaction_id)
		 BEGIN SELECT RAISE(ABORT,'immutable settlement receipt'); END;
CREATE TRIGGER trg_transaction_refunds_immutable_identity
		BEFORE UPDATE OF amount_cents, workspace_id, transaction_id ON transaction_refunds
		FOR EACH ROW
		BEGIN
			SELECT RAISE(ABORT, 'transaction refund identity and amount are immutable');
		END;
CREATE TRIGGER trg_transaction_refunds_no_direct_delete
		BEFORE DELETE ON transaction_refunds
		FOR EACH ROW
		WHEN EXISTS (
			SELECT 1 FROM transactions t
			WHERE t.id = OLD.transaction_id AND t.workspace_id = OLD.workspace_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'direct transaction refund delete not allowed');
		END;
CREATE TRIGGER trg_transaction_refunds_no_reactivation
		BEFORE UPDATE OF reversed_at, reversed_by ON transaction_refunds
		FOR EACH ROW WHEN OLD.reversed_at IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'reversed transaction refund is immutable');
		END;
CREATE TRIGGER trg_transaction_refunds_validate_insert
		BEFORE INSERT ON transaction_refunds
		FOR EACH ROW
		WHEN typeof(NEW.amount_cents) != 'integer'
		  OR NEW.amount_cents <= 0
		  OR NEW.reversed_at IS NOT NULL
		  OR NEW.reversed_by IS NOT NULL
		  OR NOT EXISTS (
			SELECT 1 FROM transactions t
			JOIN invoices i ON i.id = t.invoice_id AND i.account_id = t.account_id
			JOIN accounts a ON a.id = i.account_id AND a.workspace_id = t.workspace_id
			WHERE t.id = NEW.transaction_id AND t.workspace_id = NEW.workspace_id
			  AND t.type = 'EXPENSE'
			  AND typeof(t.amount) = 'integer' AND t.amount > 0
		  )
		  OR NOT (COALESCE((
		WITH RECURSIVE
		ordered AS (
			SELECT ROW_NUMBER() OVER (ORDER BY source_order, id) AS n, amount_cents
			FROM (SELECT 0 AS source_order, '' AS id, NEW.amount_cents AS amount_cents UNION ALL SELECT 1 AS source_order, r.id, r.amount_cents
		FROM transaction_refunds r
		WHERE r.transaction_id = NEW.transaction_id AND r.workspace_id = NEW.workspace_id
		  AND r.reversed_at IS NULL)
		),
		remaining(n, cents) AS (
			SELECT 0, (SELECT t.amount FROM transactions t WHERE t.id = NEW.transaction_id AND t.workspace_id = NEW.workspace_id)
			UNION ALL
			SELECT o.n, CASE
				WHEN typeof(o.amount_cents) = 'integer'
				 AND o.amount_cents > 0 AND o.amount_cents <= remaining.cents
				THEN remaining.cents - o.amount_cents ELSE -1 END
			FROM remaining JOIN ordered o ON o.n = remaining.n + 1
			WHERE remaining.cents >= 0
		)
		SELECT (SELECT MAX(n) FROM remaining) = (SELECT COUNT(*) FROM ordered)
		  AND NOT EXISTS (SELECT 1 FROM remaining WHERE cents < 0)
	), 0))
		BEGIN
			SELECT RAISE(ABORT, 'invalid transaction refund');
		END;
CREATE TRIGGER trg_transactions_no_recurring_terminal_insert
BEFORE INSERT ON transactions
WHEN NEW.series_id IS NOT NULL
 AND NEW.recurrence_sequence IS NOT NULL
 AND (
    EXISTS (
        SELECT 1 FROM recurring_occurrence_skips s
        WHERE s.workspace_id = NEW.workspace_id
          AND s.series_id = NEW.series_id
          AND s.recurrence_sequence = NEW.recurrence_sequence
    )
    OR EXISTS (
        SELECT 1 FROM recurring_occurrence_blocks b
        WHERE b.workspace_id = NEW.workspace_id
          AND b.series_id = NEW.series_id
          AND b.recurrence_sequence = NEW.recurrence_sequence
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: tombstone exists');
END;
CREATE TRIGGER trg_transactions_no_recurring_terminal_update
BEFORE UPDATE OF workspace_id, series_id, recurrence_sequence ON transactions
WHEN NEW.series_id IS NOT NULL
 AND NEW.recurrence_sequence IS NOT NULL
 AND (
    EXISTS (
        SELECT 1 FROM recurring_occurrence_skips s
        WHERE s.workspace_id = NEW.workspace_id
          AND s.series_id = NEW.series_id
          AND s.recurrence_sequence = NEW.recurrence_sequence
    )
    OR EXISTS (
        SELECT 1 FROM recurring_occurrence_blocks b
        WHERE b.workspace_id = NEW.workspace_id
          AND b.series_id = NEW.series_id
          AND b.recurrence_sequence = NEW.recurrence_sequence
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'recurring occurrence terminal conflict: tombstone exists');
END;
CREATE TRIGGER trg_transactions_refund_floor
		BEFORE UPDATE OF amount ON transactions
		FOR EACH ROW
		WHEN EXISTS (
			SELECT 1 FROM transaction_refunds r
			WHERE r.transaction_id = OLD.id AND r.workspace_id = OLD.workspace_id
		)
		AND (typeof(NEW.amount) != 'integer' OR NEW.amount <= 0 OR NOT (COALESCE((
		WITH RECURSIVE
		ordered AS (
			SELECT ROW_NUMBER() OVER (ORDER BY source_order, id) AS n, amount_cents
			FROM (SELECT 1 AS source_order, r.id, r.amount_cents
		FROM transaction_refunds r
		WHERE r.transaction_id = OLD.id AND r.workspace_id = OLD.workspace_id
		  AND r.reversed_at IS NULL)
		),
		remaining(n, cents) AS (
			SELECT 0, NEW.amount
			UNION ALL
			SELECT o.n, CASE
				WHEN typeof(o.amount_cents) = 'integer'
				 AND o.amount_cents > 0 AND o.amount_cents <= remaining.cents
				THEN remaining.cents - o.amount_cents ELSE -1 END
			FROM remaining JOIN ordered o ON o.n = remaining.n + 1
			WHERE remaining.cents >= 0
		)
		SELECT (SELECT MAX(n) FROM remaining) = (SELECT COUNT(*) FROM ordered)
		  AND NOT EXISTS (SELECT 1 FROM remaining WHERE cents < 0)
	), 0)))
		BEGIN
			SELECT RAISE(ABORT, 'transaction amount below active refunds or invalid type');
		END;
CREATE TRIGGER trg_transactions_refund_parent_identity
		BEFORE UPDATE OF workspace_id, type, invoice_id, account_id ON transactions
		FOR EACH ROW
		WHEN EXISTS (
			SELECT 1 FROM transaction_refunds r
			WHERE r.transaction_id = OLD.id AND r.workspace_id = OLD.workspace_id
		)
		AND (
			NEW.workspace_id != OLD.workspace_id
			OR NEW.type != 'EXPENSE'
			OR NEW.invoice_id IS NULL
			OR NOT EXISTS (
				SELECT 1 FROM invoices i
				JOIN accounts a ON a.id = i.account_id AND a.workspace_id = NEW.workspace_id
				WHERE i.id = NEW.invoice_id AND i.account_id = NEW.account_id
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'transaction refund parent identity would become invalid');
		END;
INSERT INTO schema_migrations(version,name,applied_at) VALUES(1,'baseline_recurrence_and_indexes',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(2,'transactions_recurrence_columns',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(3,'users_categories_notifications_additions',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(4,'sessions_table_for_auth',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(5,'auth_hardening_status_activation_and_session_workspace',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(6,'users_profile_photo_path',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(7,'categories_macro_group',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(8,'categories_parent_and_nullable_macro_group',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(9,'users_default_workspace_id',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(10,'security_2fa_preauth_and_auth_audit',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(11,'b2b_workspaces_contacts_and_transaction_status',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(12,'categories_macro_group_free_text',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(13,'contacts_custom_client_id_unique',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(14,'accounts_provider_slug_and_color',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(15,'workspaces_theme_token',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(16,'workspace_members_custom_permissions',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(17,'workspaces_logo_columns_and_security_logs',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(18,'workspaces_smtp_and_alert_preferences',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(19,'user_notifications_for_security_alerts',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(20,'transaction_engine_hardening_indexes',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(21,'recurring_rules_total_occurrences',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(22,'invoices_reference_by_due_date',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(23,'credit_card_transactions_invoice_backfill',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(24,'transactions_recurring_unique_duplicate_prevention',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(25,'transactions_recurring_drop_unique_index',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(26,'cost_limits_alert_threshold',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(27,'cost_limits_timestamps',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(28,'workspace_members_custom_permissions_registry_guard',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(29,'boxes_target_date',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(30,'box_virtual_ledger_release_events',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(31,'box_virtual_ledger_consume_reversal_audit_fields',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(32,'boxes_monthly_yield_rate',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(33,'sessions_remember_me_flags',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(34,'user_notifications_workspace_isolation',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(35,'accounts_icon_and_wallet_type',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(36,'accounts_archived_at',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(37,'auth_lockouts',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(38,'temporary_password_recovery',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(39,'invoice_payments_schema',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(40,'accounts_sort_order',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(41,'boxes_nullable_category',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(42,'add_users_deleted_columns',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(43,'add_transactions_paid_at',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(44,'add_transaction_status_history',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(45,'credit_cards_default_payment_account',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(46,'scheduled_transaction_payments',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(47,'notification_dedupe_key',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(48,'transaction_status_history_source',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(49,'business_canonical_categories_repair',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(50,'recurring_series_versioning_base',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(51,'recurring_materialization_state',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(52,'civil_date_columns',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(53,'recurring_rules_active_leaf_invariant',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(54,'recurring_occurrence_skips',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(55,'box_monthly_recharge_identity',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(56,'box_contribution_lifecycle',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(57,'workspace_monthly_planning_references',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(58,'invoice_payment_reversal_identity',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(59,'recurring_occurrence_blocks',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(60,'transaction_refunds',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(61,'invoice_general_refunds',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(62,'auth_mfa_step_up_and_totp_replay_protection',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(63,'session_recent_reauthentication',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(64,'account_balance_adjustments',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(65,'investments_and_valuations',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(66,'icon_keys_for_categories_limits_and_boxes',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(67,'transaction_refund_invariants',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(68,'transaction_refund_parent_hardening',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(70,'investment_purchases',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(71,'investment_withdrawals',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(72,'investment_reference_revisions',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(73,'investment_financial_integration',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(74,'investment_settlements',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(75,'investment_settlement_financial',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(76,'investment_result_classification',1);
INSERT INTO schema_migrations(version,name,applied_at) VALUES(77,'investment_adoption_confirmation',1);
